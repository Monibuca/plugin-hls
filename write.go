package hls

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	. "github.com/Monibuca/engine/v3"
	"github.com/Monibuca/utils/v3"
	"github.com/Monibuca/utils/v3/codec"
	"github.com/Monibuca/utils/v3/codec/mpegts"
)

var memoryTs sync.Map

func writeHLS(r *Stream) {
	var err error
	var hls_fragment int64       // hls fragment
	var hls_segment_count uint32 // hls segment count
	var vwrite_time uint32
	var video_cc, audio_cc uint16
	var outStream = Subscriber{ID: "HLSWriter", Type: "HLS"}

	var ring = NewRing_Video()

	if err = outStream.Subscribe(r.StreamPath); err != nil {
		utils.Println(err)
		return
	}
	vt := outStream.WaitVideoTrack("h264")
	at := outStream.WaitAudioTrack("aac")
	if err != nil {
		return
	}
	var asc codec.AudioSpecificConfig
	if at != nil {
		asc, err = decodeAudioSpecificConfig(at.ExtraData)
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
		Version:        3,
		Sequence:       0,
		Targetduration: int(hls_fragment / 666), // hlsFragment * 1.5 / 1000
	}

	hls_path := filepath.Join(config.Path, r.StreamPath)
	hls_m3u8_name := hls_path + ".m3u8"
	os.MkdirAll(hls_path, 0755)
	if err = hls_playlist.Init(hls_m3u8_name); err != nil {
		log.Println(err)
		return
	}

	hls_segment_data := &bytes.Buffer{}
	outStream.OnVideo = func(pack VideoPack) {
		packet, err := VideoPacketToPES(pack, vt.ExtraData.NALUs[0], vt.ExtraData.NALUs[1])
		if err != nil {
			return
		}
		if pack.IDR {
			// 当前的时间戳减去上一个ts切片的时间戳
			if int64(pack.Timestamp-vwrite_time) >= hls_fragment {
				//fmt.Println("time :", video.Timestamp, tsSegmentTimestamp)

				tsFilename := strconv.FormatInt(time.Now().Unix(), 10) + ".ts"

				tsData := hls_segment_data.Bytes()
				tsFilePath := filepath.Join(hls_path, tsFilename)
				if config.EnableWrite {
					if err = writeHlsTsSegmentFile(tsFilePath, tsData); err != nil {
						return
					}
				}
				if config.EnableMemory {
					ring.GetBuffer().Write(tsData)
					ring.Current.Payload = []byte(tsFilePath)
					memoryTs.Store(tsFilePath, ring.Current)
					if ring.NextW(); len(ring.Current.Payload) > 0 {
						memoryTs.Delete(string(ring.Current.Payload))
					}
				}
				inf := PlaylistInf{
					Duration: float64((pack.Timestamp - vwrite_time) / 1000),
					Title:    filepath.Base(hls_path) + "/" + tsFilename,
				}

				if hls_segment_count >= uint32(config.Window) {
					if err = hls_playlist.UpdateInf(hls_m3u8_name, hls_m3u8_name+".tmp", inf); err != nil {
						return
					}
				} else {
					if err = hls_playlist.WriteInf(hls_m3u8_name, inf); err != nil {
						return
					}
				}

				hls_segment_count++
				vwrite_time = pack.Timestamp
				hls_segment_data.Reset()
			}
		}

		frame := new(mpegts.MpegtsPESFrame)
		frame.Pid = 0x101
		frame.IsKeyFrame = pack.IDR
		frame.ContinuityCounter = byte(video_cc % 16)
		frame.ProgramClockReferenceBase = uint64(pack.Timestamp) * 90
		if err = mpegts.WritePESPacket(hls_segment_data, frame, packet); err != nil {
			return
		}

		video_cc = uint16(frame.ContinuityCounter)
	}
	outStream.OnAudio = func(pack AudioPack) {
		var packet mpegts.MpegTsPESPacket
		if packet, err = AudioPacketToPES(pack.Timestamp, pack.Raw, asc); err != nil {
			return
		}

		frame := new(mpegts.MpegtsPESFrame)
		frame.Pid = 0x102
		frame.IsKeyFrame = false
		frame.ContinuityCounter = byte(audio_cc % 16)
		//frame.ProgramClockReferenceBase = 0
		if err = mpegts.WritePESPacket(hls_segment_data, frame, packet); err != nil {
			return
		}
		audio_cc = uint16(frame.ContinuityCounter)
	}
	outStream.Play(at, vt)
	if config.EnableMemory {
		for i := byte(0); i <= 255; i++ {
			memoryTs.Delete(string(ring.GetAt(i).Payload))
		}
	}
}
