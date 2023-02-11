package hls

import (
	"bytes"
	"container/ring"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/codec/mpegts"
	"m7s.live/engine/v4/track"
	"m7s.live/engine/v4/util"
)

var memoryTs sync.Map
var memoryM3u8 sync.Map

type HLSWriter struct {
	m3u8Name string
	bytes.Buffer
	sync.RWMutex
	playlist           Playlist
	infoRing           *ring.Ring
	hls_fragment       time.Duration
	hls_segment_count  uint32 // hls segment count
	vwrite_time        time.Duration
	video_cc, audio_cc byte
	vcodec             codec.VideoCodecID
	acodec             codec.AudioCodecID
	pool               util.BytesPool
	currentTs          *MemoryTs
	Subscriber
}

func (hls *HLSWriter) Start(r *Stream) {
	hls.IsInternal = true
	hls.pool = make(util.BytesPool, 17)
	hls.currentTs = &MemoryTs{
		BytesPool: hls.pool,
	}
	if err := HLSPlugin.Subscribe(r.Path, hls); err != nil {
		HLSPlugin.Error("HLS Subscribe", zap.Error(err))
		return
	}
	if hls.VideoReader.Track != nil {
		hls.m3u8Name = r.Path + "/" + hls.VideoReader.Track.Name
	} else if hls.AudioReader.Track != nil {
		hls.m3u8Name = r.Path + "/" + hls.AudioReader.Track.Name
	}
	memoryM3u8.Store(r.Path, hls.m3u8Name)
	hls.PlayRaw()
	memoryM3u8.Delete(r.Path)
	memoryM3u8.Delete(hls.m3u8Name)
	hls.infoRing.Do(func(i interface{}) {
		if i != nil {
			memoryTs.Delete(i.(PlaylistInf).FilePath)
		}
	})
}

func (hls *HLSWriter) OnEvent(event any) {
	var err error
	defer func() {
		if err != nil {
			hls.Stop()
		}
	}()
	switch v := event.(type) {
	case *HLSWriter:
		if hlsConfig.Fragment > 0 {
			hls.hls_fragment = hlsConfig.Fragment
		} else {
			hls.hls_fragment = time.Second * 10
		}
		hls.playlist = Playlist{
			Writer:         hls,
			Version:        3,
			Sequence:       0,
			Targetduration: int(hls.hls_fragment / time.Millisecond / 666), // hlsFragment * 1.5 / 1000
		}
		if err = hls.playlist.Init(); err != nil {
			return
		}

	case *track.Video:
		hls.vcodec = v.CodecID
		hls.currentTs.PMT.Reset()
		mpegts.WritePMTPacket(&hls.currentTs.PMT, hls.vcodec, hls.acodec)
		hls.AddTrack(v)

	case *track.Audio:
		if v.CodecID == codec.CodecID_AAC {
			hls.acodec = v.CodecID
			hls.currentTs.PMT.Reset()
			mpegts.WritePMTPacket(&hls.currentTs.PMT, hls.vcodec, hls.acodec)
			hls.AddTrack(v)
		}
	case AudioFrame:
		pes := &mpegts.MpegtsPESFrame{
			Pid:                       mpegts.PID_AUDIO,
			IsKeyFrame:                false,
			ContinuityCounter:         hls.audio_cc,
			ProgramClockReferenceBase: uint64(v.PTS),
		}
		hls.currentTs.WriteAudioFrame(&v, &hls.Audio.AudioSpecificConfig, pes)
		hls.audio_cc = pes.ContinuityCounter
	case VideoFrame:
		if ts := time.Millisecond * time.Duration(v.AbsTime); v.IFrame {
			// 当前的时间戳减去上一个ts切片的时间戳
			if ts-hls.vwrite_time >= hls.hls_fragment {
				//fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)
				tsFilename := strconv.FormatInt(time.Now().Unix(), 10) + ".ts"
				tsFilePath := hls.Stream.Path + "/" + tsFilename
				memoryTs.Store(tsFilePath, hls.currentTs)
				// println(hls.currentTs.Length)
				hls.currentTs = &MemoryTs{
					BytesPool: hls.pool,
				}
				inf := PlaylistInf{
					//浮点计算精度
					Duration: (ts - hls.vwrite_time).Seconds(),
					Title:    tsFilename,
					FilePath: tsFilePath,
				}
				hls.Lock()
				defer hls.Unlock()
				if hls.hls_segment_count >= uint32(hlsConfig.Window) {
					hls.Reset()
					if err = hls.playlist.Init(); err != nil {
						return
					}
					if mts, loaded := memoryTs.LoadAndDelete(hls.infoRing.Value.(PlaylistInf).FilePath); loaded {
						mts.(*MemoryTs).Recycle()
					}
					hls.infoRing.Value = inf
					hls.infoRing = hls.infoRing.Next()
					hls.infoRing.Do(func(i interface{}) {
						hls.playlist.WriteInf(i.(PlaylistInf))
					})
				} else {
					hls.infoRing.Value = inf
					hls.infoRing = hls.infoRing.Next()
					if err = hls.playlist.WriteInf(inf); err != nil {
						return
					}
				}
				hls.hls_segment_count++
				hls.vwrite_time = ts
				if hls.playlist.tsCount > 0 {
					memoryM3u8.LoadOrStore(hls.m3u8Name, hls)
				}
			}
		}

		pes := &mpegts.MpegtsPESFrame{
			Pid:                       mpegts.PID_VIDEO,
			IsKeyFrame:                v.IFrame,
			ContinuityCounter:         hls.video_cc,
			ProgramClockReferenceBase: uint64(v.PTS),
		}
		if err = hls.currentTs.WriteVideoFrame(&v, hls.Video.ParamaterSets, pes); err != nil {
			return
		}

		hls.video_cc = pes.ContinuityCounter
	default:
		hls.Subscriber.OnEvent(event)
	}
}
