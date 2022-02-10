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

	. "github.com/Monibuca/engine/v4"
	"github.com/Monibuca/engine/v4/codec"
	"github.com/Monibuca/engine/v4/codec/mpegts"
)

var memoryTs sync.Map
var memoryM3u8 sync.Map

func (config *HLSConfig) writeHLS(r *Stream) {
	if config.filterReg != nil && !config.filterReg.MatchString(r.Path) {
		return
	}
	var m3u8Buffer bytes.Buffer
	var infoRing = ring.New(config.Window)

	memoryM3u8.Store(r.Path, &m3u8Buffer)
	defer memoryM3u8.Delete(r.Path)
	var err error
	var hls_fragment int64       // hls fragment
	var hls_segment_count uint32 // hls segment count
	var vwrite_time uint32
	var video_cc, audio_cc uint16
	var outStream = Subscriber{ID: "HLSWriter", Type: "HLS"}

	if !outStream.Subscribe(r.Path, hlsConfig.Subscribe) {
		return
	}
	vt := outStream.WaitVideoTrack("h264")
	at := outStream.WaitAudioTrack("aac")

	var asc codec.AudioSpecificConfig
	if at != nil {
		asc, err = decodeAudioSpecificConfig([]byte(at.DecoderConfiguration.AVCC))
	}
	if err != nil {
		return
	}
	if config.Fragment > 0 {
		hls_fragment = config.Fragment * 1000
	} else {
		hls_fragment = 10000
	}

	hls_playlist := Playlist{
		Writer:         &m3u8Buffer,
		Version:        3,
		Sequence:       0,
		Targetduration: int(hls_fragment / 666), // hlsFragment * 1.5 / 1000
	}
	hls_path := filepath.Join(config.Path, r.Path, fmt.Sprintf("%d.m3u8", time.Now().Unix()))
	os.MkdirAll(filepath.Dir(hls_path), 0755)
	var file *os.File
	file, err = os.OpenFile(hls_path, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return
	}
	defer file.Close()
	record_playlist := Playlist{
		Writer:         file,
		Version:        3,
		Sequence:       0,
		Targetduration: int(hls_fragment / 666), // hlsFragment * 1.5 / 1000
	}
	if err = hls_playlist.Init(); err != nil {
		return
	}
	if err = record_playlist.Init(); err != nil {
		return
	}
	hls_segment_data := &bytes.Buffer{}
	var packet mpegts.MpegTsPESPacket
	outStream.OnVideo = func(frame *VideoFrame) (err error) {
		packet, err = VideoPacketToPES(frame, vt.DecoderConfiguration)
		if err != nil {
			return err
		}
		ts := frame.DTS / 90
		if frame.IFrame {
			// 当前的时间戳减去上一个ts切片的时间戳
			if int64(ts-vwrite_time) >= hls_fragment {
				//fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)

				tsFilename := strconv.FormatInt(time.Now().Unix(), 10) + ".ts"

				tsData := hls_segment_data.Bytes()
				tsFilePath := filepath.Join(filepath.Dir(hls_path), tsFilename)
				if config.EnableWrite {
					if err = writeHlsTsSegmentFile(tsFilePath, tsData); err != nil {
						return err
					}
				}
				if config.EnableMemory {
					memoryTs.Store(tsFilePath, tsData)
				}
				inf := PlaylistInf{
					//浮点计算精度
					Duration: float64((ts - vwrite_time) / 1000.0),
					Title:    filepath.Base(filepath.Dir(hls_path)) + "/" + tsFilename,
					FilePath: tsFilePath,
				}

				if hls_segment_count >= uint32(config.Window) {
					m3u8Buffer.Reset()
					if err = hls_playlist.Init(); err != nil {
						return err
					}
					memoryTs.Delete(infoRing.Value.(PlaylistInf).FilePath)
					infoRing.Value = inf
					infoRing = infoRing.Next()
					infoRing.Do(func(i interface{}) {
						hls_playlist.WriteInf(i.(PlaylistInf))
					})
				} else {
					infoRing.Value = inf
					infoRing = infoRing.Next()
					if err = hls_playlist.WriteInf(inf); err != nil {
						return err
					}
				}
				inf.Title = tsFilename
				if err = record_playlist.WriteInf(inf); err != nil {
					return err
				}
				hls_segment_count++
				vwrite_time = ts
				hls_segment_data.Reset()
			}
		}

		pes := &mpegts.MpegtsPESFrame{
			Pid:                       0x101,
			IsKeyFrame:                frame.IFrame,
			ContinuityCounter:         byte(video_cc % 16),
			ProgramClockReferenceBase: uint64(frame.DTS),
		}
		if err = mpegts.WritePESPacket(hls_segment_data, pes, packet); err != nil {
			return err
		}
		video_cc = uint16(pes.ContinuityCounter)
		return nil
	}
	outStream.OnAudio = func(frame *AudioFrame) (err error) {
		if packet, err = AudioPacketToPES(frame, asc); err != nil {
			return
		}
		pes := &mpegts.MpegtsPESFrame{
			Pid:                       0x102,
			IsKeyFrame:                false,
			ContinuityCounter:         byte(audio_cc % 16),
			ProgramClockReferenceBase: uint64(frame.DTS),
		}
		//frame.ProgramClockReferenceBase = 0
		if err = mpegts.WritePESPacket(hls_segment_data, pes, packet); err != nil {
			return
		}
		audio_cc = uint16(pes.ContinuityCounter)
		return
	}
	outStream.Play(at, vt)
	if config.EnableMemory {
		infoRing.Do(func(i interface{}) {
			if i != nil {
				memoryTs.Delete(i.(PlaylistInf).FilePath)
			}
		})
	}
}
