package hls

import (
	"bytes"
	"container/ring"
	"net"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/codec/mpegts"
	"m7s.live/engine/v4/track"
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
	hls_segment_data   *bytes.Buffer
	packet             mpegts.MpegTsPESPacket
	vcodec             codec.VideoCodecID
	acodec             codec.AudioCodecID
	pmt                []byte
	Subscriber
}

func (hls *HLSWriter) Start(r *Stream) {
	hls.IsInternal = true
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
		hls.hls_segment_data = new(bytes.Buffer)
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
		var buffer bytes.Buffer
		mpegts.WritePMTPacket(&buffer, hls.vcodec, hls.acodec)
		hls.pmt = buffer.Bytes()
		hls.AddTrack(v)

	case *track.Audio:
		if v.CodecID == codec.CodecID_AAC {
			hls.acodec = v.CodecID
			var buffer bytes.Buffer
			mpegts.WritePMTPacket(&buffer, hls.vcodec, hls.acodec)
			hls.pmt = buffer.Bytes()
			hls.AddTrack(v)
		}
	case AudioFrame:
		if hls.packet, err = AudioPacketToPES(&v, &hls.Audio.AudioSpecificConfig); err != nil {
			return
		}
		pes := &mpegts.MpegtsPESFrame{
			Pid:                       mpegts.PID_AUDIO,
			IsKeyFrame:                false,
			ContinuityCounter:         hls.audio_cc,
			ProgramClockReferenceBase: uint64(v.PTS),
		}
		//frame.ProgramClockReferenceBase = 0
		if err = mpegts.WritePESPacket(hls.hls_segment_data, pes, hls.packet); err != nil {
			return
		}
		hls.audio_cc = pes.ContinuityCounter
	case VideoFrame:
		hls.packet, err = VideoPacketToPES(&v, hls.Video)
		if err != nil {
			return
		}
		if ts := time.Millisecond * time.Duration(v.AbsTime); v.IFrame {
			// 当前的时间戳减去上一个ts切片的时间戳
			if ts-hls.vwrite_time >= hls.hls_fragment {
				//fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)
				tsFilename := strconv.FormatInt(time.Now().Unix(), 10) + ".ts"
				tsFilePath := hls.Stream.Path + "/" + tsFilename
				memoryTs.Store(tsFilePath, net.Buffers{
					mpegts.DefaultPATPacket,
					hls.pmt,
					hls.hls_segment_data.Bytes(),
				})
				hls.hls_segment_data = new(bytes.Buffer)
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
					memoryTs.Delete(hls.infoRing.Value.(PlaylistInf).FilePath)
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
				inf.Title = tsFilename
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
		if err = mpegts.WritePESPacket(hls.hls_segment_data, pes, hls.packet); err != nil {
			return
		}
		hls.video_cc = pes.ContinuityCounter
	default:
		hls.Subscriber.OnEvent(event)
	}
}
