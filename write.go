package hls

import (
	"container/ring"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec/mpegts"
	"m7s.live/engine/v4/track"
	"m7s.live/engine/v4/util"
)

var memoryTs sync.Map
var memoryM3u8 sync.Map

type TrackReader struct {
	sync.RWMutex
	M3u8 util.Buffer
	pes  *mpegts.MpegtsPESFrame
	ts   *MemoryTs
	track.AVRingReader
	write_time        time.Duration
	m3u8Name          string
	hls_segment_count uint32 // hls segment count
	playlist          Playlist
	infoRing          *ring.Ring
}

func (tr *TrackReader) init(hls *HLSWriter, media *track.Media, pid uint16) {
	tr.ts = &MemoryTs{
		BytesPool: hls.pool,
	}
	tr.pes = &mpegts.MpegtsPESFrame{
		Pid: pid,
	}
	tr.infoRing = ring.New(hlsConfig.Window)
	tr.m3u8Name = hls.Stream.Path + "/" + media.Name
	tr.AVRingReader = hls.CreateTrackReader(media)
	tr.playlist = Playlist{
		Writer:         &tr.M3u8,
		Version:        3,
		Sequence:       0,
		Targetduration: int(hlsConfig.Fragment / time.Millisecond / 666), // hlsFragment * 1.5 / 1000
	}
}

type AudioTrackReader struct {
	TrackReader
	*track.Audio
}

type VideoTrackReader struct {
	TrackReader
	*track.Video
}

type HLSWriter struct {
	pool         util.BytesPool
	audio_tracks []*AudioTrackReader
	video_tracks []*VideoTrackReader
	defaultVideo *VideoTrackReader
	Subscriber
}

func (hls *HLSWriter) Start(r *Stream) {
	hls.IsInternal = true
	hls.pool = make(util.BytesPool, 17)
	if err := HLSPlugin.Subscribe(r.Path, hls); err != nil {
		HLSPlugin.Error("HLS Subscribe", zap.Error(err))
		return
	}
	hls.ReadTrack()
	memoryM3u8.Delete(r.Path)
	for _, t := range hls.video_tracks {
		memoryM3u8.Delete(t.m3u8Name)
		t.infoRing.Do(func(i interface{}) {
			if i != nil {
				memoryTs.Delete(i.(PlaylistInf).FilePath)
			}
		})
	}
	for _, t := range hls.audio_tracks {
		memoryM3u8.Delete(t.m3u8Name)
		t.infoRing.Do(func(i interface{}) {
			if i != nil {
				memoryTs.Delete(i.(PlaylistInf).FilePath)
			}
		})
	}
}
func (hls *HLSWriter) ReadTrack() {
	var defaultAudio *AudioTrackReader
	var defaultVideo *VideoTrackReader
	for _, t := range hls.video_tracks {
		if defaultVideo == nil {
			defaultVideo = t
			hls.defaultVideo = t
		}
	}
	for _, t := range hls.audio_tracks {
		if defaultAudio == nil {
			defaultAudio = t
		}
	}
	var audioGroup string
	m3u8 := `#EXTM3U
#EXT-X-VERSION:3`
	if defaultAudio != nil {
		audioGroup = `,AUDIO="audio"`
		m3u8 += fmt.Sprintf(`
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="%s",DEFAULT=YES,AUTOSELECT=YES,URI="%s/%s.m3u8"`, defaultAudio.Track.Name, hls.Stream.StreamName, defaultAudio.Track.Name)
	}
	if defaultVideo != nil {
		m3u8 += fmt.Sprintf(`
#EXT-X-STREAM-INF:BANDWIDTH=2962000,NAME="%s",RESOLUTION=%dx%d%s
%s/%s.m3u8`, defaultVideo.Track.Name, defaultVideo.Width, defaultVideo.Height, audioGroup, hls.Stream.StreamName, defaultVideo.Track.Name)
	}
	// 存一个默认的m3u8
	memoryM3u8.Store(hls.Stream.Path, m3u8)
	for hls.IO.Err() == nil {
		for _, t := range hls.video_tracks {
			for {
				frame := t.TryRead()
				if frame == nil {
					break
				}
				if frame.IFrame {
					t.TrackReader.frag(hls.Stream.Path, frame.AbsTime)
				}
				t.pes.IsKeyFrame = frame.IFrame
				t.ts.WriteVideoFrame(VideoFrame{frame, t.Video, frame.AbsTime, frame.PTS, frame.DTS}, t.pes)
				t.MoveNext()
			}
		}
		for _, t := range hls.audio_tracks {
			for {
				frame := t.TryRead()
				if frame == nil {
					break
				}
				t.TrackReader.frag(hls.Stream.Path, frame.AbsTime)
				t.pes.IsKeyFrame = false
				t.ts.WriteAudioFrame(AudioFrame{frame, t.Audio, frame.AbsTime, frame.PTS, frame.DTS}, t.pes)
				t.MoveNext()
			}
		}
		time.Sleep(time.Millisecond * 10)
	}
}

func (t *TrackReader) frag(streamPath string, absTime uint32) (err error) {
	ts := time.Millisecond * time.Duration(absTime)
	// 当前的时间戳减去上一个ts切片的时间戳
	if dur := ts - t.write_time; dur >= hlsConfig.Fragment {
		// fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)
		tsFilename := t.Track.Name + strconv.FormatInt(time.Now().Unix(), 10) + ".ts"
		tsFilePath := streamPath + "/" + tsFilename
		memoryTs.Store(tsFilePath, t.ts)
		// println(hls.currentTs.Length)
		t.ts = &MemoryTs{
			BytesPool: t.ts.BytesPool,
			PMT:       t.ts.PMT,
		}
		if t.playlist.Targetduration < int(dur.Seconds()) {
			t.playlist.Targetduration = int(math.Ceil(dur.Seconds()))
		}
		if t.M3u8.Len() == 0 {
			t.playlist.Init()
		}
		inf := PlaylistInf{
			//浮点计算精度
			Duration: dur.Seconds(),
			Title:    tsFilename,
			FilePath: tsFilePath,
		}
		t.Lock()
		defer t.Unlock()
		if t.hls_segment_count >= uint32(hlsConfig.Window) {
			t.M3u8.Reset()
			if err = t.playlist.Init(); err != nil {
				return
			}
			if mts, loaded := memoryTs.LoadAndDelete(t.infoRing.Value.(PlaylistInf).FilePath); loaded {
				mts.(*MemoryTs).Recycle()
			}
			t.infoRing.Value = inf
			t.infoRing = t.infoRing.Next()
			t.infoRing.Do(func(i interface{}) {
				t.playlist.WriteInf(i.(PlaylistInf))
			})
		} else {
			t.infoRing.Value = inf
			t.infoRing = t.infoRing.Next()
			if err = t.playlist.WriteInf(inf); err != nil {
				return
			}
		}
		t.hls_segment_count++
		t.write_time = ts
		if t.playlist.tsCount > 0 {
			memoryM3u8.LoadOrStore(t.m3u8Name, t)
		}
	}
	return
}

func (hls *HLSWriter) OnEvent(event any) {
	var err error
	defer func() {
		if err != nil {
			hls.Warn("write stop", zap.Error(err))
			hls.Stop()
		}
	}()
	switch v := event.(type) {
	case *track.Video:
		track := &VideoTrackReader{
			Video: v,
		}
		track.init(hls, &v.Media, mpegts.PID_VIDEO)
		track.ts.WritePMTPacket(0, v.CodecID)
		track.Ring = track.IDRing
		hls.video_tracks = append(hls.video_tracks, track)
	case *track.Audio:
		track := &AudioTrackReader{
			Audio: v,
		}
		track.init(hls, &v.Media, mpegts.PID_AUDIO)
		track.ts.WritePMTPacket(v.CodecID, 0)
		if hls.defaultVideo != nil {
			for track.IDRing == nil && !hls.IsClosed() {
				time.Sleep(time.Millisecond * 10)
			}
			track.Ring = track.IDRing
		} else {
			track.Ring = track.Track.Ring
		}
		hls.audio_tracks = append(hls.audio_tracks, track)
	default:
		hls.Subscriber.OnEvent(event)
	}
}
