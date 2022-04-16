package hls

import (
	"bytes"
	"container/ring"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/codec/mpegts"
)

var memoryTs sync.Map
var memoryM3u8 sync.Map

type HLSWriter struct {
	hls_path           string
	m3u8Buffer         bytes.Buffer
	hls_playlist       Playlist
	record_playlist    Playlist
	infoRing           *ring.Ring
	asc                codec.AudioSpecificConfig
	hls_fragment       int64
	hls_segment_count  uint32 // hls segment count
	vwrite_time        uint32
	video_cc, audio_cc uint16
	hls_segment_data   bytes.Buffer
	packet             mpegts.MpegTsPESPacket
	Subscriber
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
			hls.hls_fragment = hlsConfig.Fragment * 1000
		} else {
			hls.hls_fragment = 10000
		}
		hls.hls_playlist = Playlist{
			Writer:         &hls.m3u8Buffer,
			Version:        3,
			Sequence:       0,
			Targetduration: int(hls.hls_fragment / 666), // hlsFragment * 1.5 / 1000
		}
		hls.hls_path = filepath.Join(hlsConfig.Path, hls.Stream.Path, fmt.Sprintf("%d.m3u8", time.Now().Unix()))
		os.MkdirAll(filepath.Dir(hls.hls_path), 0755)
		var file *os.File
		file, err = os.OpenFile(hls.hls_path, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			return
		}
		defer file.Close()
		hls.record_playlist = Playlist{
			Writer:         file,
			Version:        3,
			Sequence:       0,
			Targetduration: int(hls.hls_fragment / 666), // hlsFragment * 1.5 / 1000
		}
		if err = hls.hls_playlist.Init(); err != nil {
			return
		}
		if err = hls.record_playlist.Init(); err != nil {
			return
		}
	case AudioDeConf:
		hls.asc, err = decodeAudioSpecificConfig(v.AVCC[0])
	case *AudioFrame:
		if hls.packet, err = AudioPacketToPES(v, hls.asc); err != nil {
			return
		}
		pes := &mpegts.MpegtsPESFrame{
			Pid:                       0x102,
			IsKeyFrame:                false,
			ContinuityCounter:         byte(hls.audio_cc % 16),
			ProgramClockReferenceBase: uint64(v.DTS),
		}
		//frame.ProgramClockReferenceBase = 0
		if err = mpegts.WritePESPacket(&hls.hls_segment_data, pes, hls.packet); err != nil {
			return
		}
		hls.audio_cc = uint16(pes.ContinuityCounter)
	case *VideoFrame:
		hls.packet, err = VideoPacketToPES(v, hls.Video.Track.DecoderConfiguration)
		if err != nil {
			return
		}
		ts := v.AbsTime
		if v.IFrame {
			// 当前的时间戳减去上一个ts切片的时间戳
			if int64(ts-hls.vwrite_time) >= hls.hls_fragment {
				//fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)

				tsFilename := strconv.FormatInt(time.Now().Unix(), 10) + ".ts"

				tsData := hls.hls_segment_data.Bytes()
				tsFilePath := filepath.Join(filepath.Dir(hls.hls_path), tsFilename)
				if hlsConfig.EnableWrite {
					if err = writeHlsTsSegmentFile(tsFilePath, tsData); err != nil {
						return
					}
				}
				if hlsConfig.EnableMemory {
					memoryTs.Store(tsFilePath, tsData)
				}
				inf := PlaylistInf{
					//浮点计算精度
					Duration: float64((ts - hls.vwrite_time) / 1000.0),
					Title:    filepath.Base(filepath.Dir(hls.hls_path)) + "/" + tsFilename,
					FilePath: tsFilePath,
				}

				if hls.hls_segment_count >= uint32(hlsConfig.Window) {
					hls.m3u8Buffer.Reset()
					if err = hls.hls_playlist.Init(); err != nil {
						return
					}
					memoryTs.Delete(hls.infoRing.Value.(PlaylistInf).FilePath)
					hls.infoRing.Value = inf
					hls.infoRing = hls.infoRing.Next()
					hls.infoRing.Do(func(i interface{}) {
						hls.hls_playlist.WriteInf(i.(PlaylistInf))
					})
				} else {
					hls.infoRing.Value = inf
					hls.infoRing = hls.infoRing.Next()
					if err = hls.hls_playlist.WriteInf(inf); err != nil {
						return
					}
				}
				inf.Title = tsFilename
				if err = hls.record_playlist.WriteInf(inf); err != nil {
					return
				}
				hls.hls_segment_count++
				hls.vwrite_time = ts
				hls.hls_segment_data.Reset()
			}
		}

		pes := &mpegts.MpegtsPESFrame{
			Pid:                       0x101,
			IsKeyFrame:                v.IFrame,
			ContinuityCounter:         byte(hls.video_cc % 16),
			ProgramClockReferenceBase: uint64(v.DTS),
		}
		if err = mpegts.WritePESPacket(&hls.hls_segment_data, pes, hls.packet); err != nil {
			return
		}
		hls.video_cc = uint16(pes.ContinuityCounter)
	default:
		hls.Subscriber.OnEvent(event)
	}
}

func (config *HLSConfig) writeHLS(r *Stream) {
	if config.filterReg != nil && !config.filterReg.MatchString(r.Path) {
		return
	}
	defer memoryM3u8.Delete(r.Path)
	var outStream = &HLSWriter{
		infoRing: ring.New(config.Window),
	}
	memoryM3u8.Store(r.Path, &outStream.m3u8Buffer)
	if plugin.SubscribeBlock(r.Path, outStream) != nil {
		return
	}
	if config.EnableMemory {
		outStream.infoRing.Do(func(i interface{}) {
			if i != nil {
				memoryTs.Delete(i.(PlaylistInf).FilePath)
			}
		})
	}
}
