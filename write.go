package hls

import (
	"container/ring"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/codec/mpegts"
	"m7s.live/engine/v4/track"
	"m7s.live/engine/v4/util"
)

var memoryTs util.Map[string, interface {
	GetTs(key string) util.Recyclable
}]
var memoryM3u8 sync.Map
var pools sync.Pool

func init() {
	pools.New = func() any {
		return make(util.BytesPool, 20)
	}
}

type TrackReader struct {
	sync.RWMutex
	M3u8 util.Buffer
	pes  *mpegts.MpegtsPESFrame
	ts   *MemoryTs
	*track.AVRingReader
	write_time        time.Duration
	m3u8Name          string
	hls_segment_count uint32 // hls segment count
	playlist          Playlist
	infoRing          *ring.Ring
	hls_playlist_count uint32
	hls_segment_window uint32
}

func (tr *TrackReader) init(hls *HLSWriter, media *track.Media, pid uint16) {
	tr.ts = &MemoryTs{
		BytesPool: hls.pool,
	}
	tr.pes = &mpegts.MpegtsPESFrame{
		Pid: pid,
	}
	tr.hls_segment_window = uint32(hlsConfig.Window) + 1
	tr.infoRing = ring.New(int(tr.hls_segment_window))
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
	Subscriber
	memoryTs     util.Map[string, util.Recyclable]
	lastReadTime time.Time
}

func (hls *HLSWriter) GetTs(key string) util.Recyclable {
	hls.lastReadTime = time.Now()
	return hls.memoryTs.Get(key)
}

func (hls *HLSWriter) Start(streamPath string) {
	hls.pool = pools.Get().(util.BytesPool)
	if err := HLSPlugin.Subscribe(streamPath, hls); err != nil {
		HLSPlugin.Error("HLS Subscribe", zap.Error(err))
		return
	}
	streamPath = strings.Split(streamPath, "?")[0]
	memoryTs.Add(streamPath, hls)
	hls.ReadTrack()
	memoryTs.Delete(streamPath)
	hls.memoryTs.Range(func(k string, v util.Recyclable) {
		v.Recycle()
	})
	pools.Put(hls.pool)
	memoryM3u8.Delete(streamPath)
	for _, t := range hls.video_tracks {
		memoryM3u8.Delete(t.m3u8Name)
	}
	for _, t := range hls.audio_tracks {
		memoryM3u8.Delete(t.m3u8Name)
	}
	if !hlsConfig.Preload {
		writingMap.Delete(streamPath)
	}
}
func (hls *HLSWriter) ReadTrack() {
	var defaultAudio *AudioTrackReader
	var defaultVideo *VideoTrackReader
	for _, t := range hls.video_tracks {
		if defaultVideo == nil {
			defaultVideo = t
			break
		}
	}
	for _, t := range hls.audio_tracks {
		if defaultAudio == nil {
			defaultAudio = t
			if defaultVideo != nil {
				for t.IDRing == nil && !hls.IsClosed() {
					time.Sleep(time.Millisecond * 10)
				}
				t.Ring = t.IDRing
			} else {
				t.Ring = t.Track.Ring
			}
			break
		}
	}
	var audioGroup string
	m3u8 := `#EXTM3U
#EXT-X-VERSION:3`
	if defaultAudio != nil {
		audioGroup = `,AUDIO="audio"`
		m3u8 += fmt.Sprintf(`
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="%s",DEFAULT=YES,AUTOSELECT=YES,URI="%s/%s.m3u8?sub=1"`, defaultAudio.Track.Name, hls.Stream.StreamName, defaultAudio.Track.Name)
	}
	if defaultVideo != nil {
		m3u8 += fmt.Sprintf(`
#EXT-X-STREAM-INF:BANDWIDTH=2962000,NAME="%s",RESOLUTION=%dx%d%s
%s/%s.m3u8?sub=1`, defaultVideo.Track.Name, defaultVideo.Width, defaultVideo.Height, audioGroup, hls.Stream.StreamName, defaultVideo.Track.Name)
	}
	// 存一个默认的m3u8
	memoryM3u8.Store(hls.Stream.Path, m3u8)
	for hls.IO.Err() == nil {
		for _, t := range hls.video_tracks {
			for {
				frame, err := t.TryRead()
				if err != nil {
					return
				}
				if frame == nil {
					break
				}
				if frame.IFrame {
					t.TrackReader.frag(hls, frame.Timestamp)
				}
				t.pes.IsKeyFrame = frame.IFrame
				t.ts.WriteVideoFrame(VideoFrame{frame, t.Video, t.AbsTime, uint32(frame.PTS), uint32(frame.DTS)}, t.pes)
			}
		}
		for _, t := range hls.audio_tracks {
			for {
				frame, err := t.TryRead()
				if err != nil {
					return
				}
				if frame == nil {
					break
				}
				t.TrackReader.frag(hls, frame.Timestamp)
				t.pes.IsKeyFrame = false
				t.ts.WriteAudioFrame(AudioFrame{frame, t.Audio, t.AbsTime, uint32(frame.PTS), uint32(frame.DTS)}, t.pes)
			}
		}
		time.Sleep(time.Millisecond * 10)
		if !hlsConfig.Preload && !hls.lastReadTime.IsZero() && time.Since(hls.lastReadTime) > time.Second*15 {
			hls.Stop(zap.String("reason", "no reader after 15s"))
		}
	}
}

func (t *TrackReader) frag(hls *HLSWriter, ts time.Duration) (err error) {
	streamPath := hls.Stream.Path
	// 当前的时间戳减去上一个ts切片的时间戳
	if dur := ts - t.write_time; dur >= hlsConfig.Fragment {
		// fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)
		if dur == ts && t.write_time == 0 {//时间戳不对的情况，首个默认为2s
			dur = time.Duration(2) * time.Second
		}
		num := uint32(t.hls_segment_count)
		tsFilename := t.Track.Name + strconv.FormatInt(time.Now().Unix(), 10) + "_" + strconv.FormatUint(uint64(num), 10) + ".ts"
		tsFilePath := streamPath + "/" + tsFilename

		// println(hls.currentTs.Length)
		t.ts = &MemoryTs{
			BytesPool: t.ts.BytesPool,
			PMT:       t.ts.PMT,
		}
		hls.memoryTs.Store(tsFilePath, t.ts)
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

		if t.hls_segment_count > 0 {
			if t.hls_playlist_count >= uint32(hlsConfig.Window) {
				t.M3u8.Reset()
				if err = t.playlist.Init(); err != nil {
					return
				}
				//playlist起点是ring.next，长度是len(ring)-1				
				for p := t.infoRing.Next(); p != t.infoRing; p = p.Next() {
					t.playlist.WriteInf(p.Value.(PlaylistInf))
				}
			} else {
				if err = t.playlist.WriteInf(t.infoRing.Prev().Value.(PlaylistInf)); err != nil {
					return
				}
			}
			memoryM3u8.LoadOrStore(t.m3u8Name, t)
			t.hls_playlist_count++
		}

		if t.hls_segment_count >= t.hls_segment_window {
			if mts, loaded := hls.memoryTs.Delete(t.infoRing.Value.(PlaylistInf).FilePath); loaded {
				mts.Recycle()
			}
			t.infoRing.Value = inf
			t.infoRing = t.infoRing.Next()
		} else {
			t.infoRing.Value = inf
			t.infoRing = t.infoRing.Next()
		}
		t.hls_segment_count++
		t.write_time = ts		

	}
	return
}

func (hls *HLSWriter) OnEvent(event any) {
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
		if v.CodecID != codec.CodecID_AAC {
			return
		}
		track := &AudioTrackReader{
			Audio: v,
		}
		track.init(hls, &v.Media, mpegts.PID_AUDIO)
		track.ts.WritePMTPacket(v.CodecID, 0)
		hls.audio_tracks = append(hls.audio_tracks, track)
	default:
		hls.Subscriber.OnEvent(event)
	}
}
