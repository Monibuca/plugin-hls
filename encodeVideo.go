package hls

import (
	"errors"
	"os"

	"github.com/Monibuca/engine/v2/avformat"
	"github.com/Monibuca/engine/v2/avformat/mpegts"
	"github.com/Monibuca/engine/v2/util"
)

func decodeAVCDecoderConfigurationRecord(video *avformat.SendPacket) (avc_dcr avformat.AVCDecoderConfigurationRecord, err error) {
	if len(video.Payload) < 13 {
		err = errors.New("decodeAVCDecoderConfigurationRecord error 1")
		return
	}

	// 如果视频的格式是AVC(H.264)的话,VideoTagHeader会多出4个字节的信息AVCPacketType 和 CompositionTime
	vft := video.VideoFrameType()
	if vft != 1 && vft != 2 {
		err = errors.New("decodeAVCDecoderConfigurationRecord error : this packet is not AVC(H264)")
		return
	}

	// AVCPacketType, 0 = AVC sequence header, 1 = AVC NALU, 2 = AVC end of sequence (lower level NALU sequence ender is not required or supported)
	if video.Payload[1] != 0 {
		err = errors.New("decodeAVCDecoderConfigurationRecord error : this packet is not AVC sequence header")
		return
	}

	// 前面有5个字节(视频信息).
	avc_dcr.ConfigurationVersion = video.Payload[4+1]
	avc_dcr.AVCProfileIndication = video.Payload[4+2]
	avc_dcr.ProfileCompatibility = video.Payload[4+3]
	avc_dcr.AVCLevelIndication = video.Payload[4+4]
	avc_dcr.Reserved1 = video.Payload[4+5] >> 2            // reserved 111111
	avc_dcr.LengthSizeMinusOne = video.Payload[4+5] & 0x03 // H.264 视频中 NALU 的长度,一般为3
	avc_dcr.Reserved2 = video.Payload[4+6] >> 5            // reserved 111

	avc_dcr.NumOfSequenceParameterSets = video.Payload[4+6] & 31                    // sps个数,一般为1
	avc_dcr.SequenceParameterSetLength = util.BigEndian.Uint16(video.Payload[4+7:]) // sps长度

	if len(video.Payload) < 4+9+int(avc_dcr.SequenceParameterSetLength)+1+2 {
		err = errors.New("decodeAVCDecoderConfigurationRecord error 2")
		return
	}
	avc_dcr.SequenceParameterSetNALUnit = video.Payload[4+9 : 4+9+int(avc_dcr.SequenceParameterSetLength)] // sps

	avc_dcr.NumOfPictureParameterSets = video.Payload[4+9+int(avc_dcr.SequenceParameterSetLength)]                           // pps个数,一般为1
	avc_dcr.PictureParameterSetLength = util.BigEndian.Uint16(video.Payload[4+9+int(avc_dcr.SequenceParameterSetLength)+1:]) // pps长度
	avc_dcr.PictureParameterSetNALUnit = video.Payload[4+9+int(avc_dcr.SequenceParameterSetLength)+1+2:]                     // pps

	return
}
func rtmpVideoPacketToPES(video *avformat.SendPacket, avc_dcr avformat.AVCDecoderConfigurationRecord) (packet mpegts.MpegTsPESPacket, err error) {
	var data []byte

	//需要对原始数据(ES),进行一些预处理,视频需要分割nalu(H264编码),并且打上sps,pps,nalu_aud信息.
	if data, err = rtmpVideoPacketToPESPreprocess(video, avc_dcr); err != nil {
		return
	}

	pktLength := len(data) + 10 + 3
	if pktLength > 0xffff {
		pktLength = 0
	}

	// cts = (pts - dts) / 90
	var cts uint32
	var avcPktType uint32
	if avcPktType, err = util.ByteToUint32N(video.Payload[1:2]); err != nil {
		return
	}

	if avcPktType == 1 {
		if cts, err = util.ByteToUint32N(video.Payload[2:5]); err != nil {
			return
		}
	}

	//cts = ((cts & 0x00FF0000) >> 16) | ((cts & 0x000000FF) << 16) | (cts & 0x0000FF00)

	packet.Header.PacketStartCodePrefix = 0x000001
	packet.Header.ConstTen = 0x80
	packet.Header.StreamID = mpegts.STREAM_ID_VIDEO
	packet.Header.PesPacketLength = uint16(pktLength)
	packet.Header.Pts = uint64(video.Timestamp+cts) * 90
	packet.Header.Dts = uint64(video.Timestamp) * 90
	packet.Header.PtsDtsFlags = 0xC0
	packet.Header.PesHeaderDataLength = 10

	packet.Payload = data

	return
}
func rtmpVideoPacketToPESPreprocess(video *avformat.SendPacket, avc_dcr avformat.AVCDecoderConfigurationRecord) (data []byte, err error) {
	// nalu array
	if data, err = rtmpVideoPacketSplitNaluAndAppendAudSPSPPS(video.Payload, &avc_dcr, uint32(avc_dcr.LengthSizeMinusOne+1)); err != nil {
		return
	}

	return
}
func writeHlsTsSegmentFile(filename string, data []byte) (err error) {
	var file *os.File

	file, err = os.OpenFile(filename, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return
	}
	defer file.Close()

	if err = mpegts.WriteDefaultPATPacket(file); err != nil {
		return
	}

	if err = mpegts.WriteDefaultPMTPacket(file); err != nil {
		return
	}

	if _, err = file.Write(data); err != nil {
		return
	}

	file.Close()

	return
}

// 原本Video包: 5 bytes + nalu_lenght1 + nalu_data1 + nalu_length2 + nalu_data2 + ... + nalu_lengthN + nalu_dataN
// Split后: 5 bytes + nalu_data1 + nalu_data2 + ... + nalu_dataN

// HLS MPEGTS 需要 Append NALU AUD
// frameType  + codecID + avcPacketType + compositionTime (4 + 4 + 8 + 24) == 5 bytes
// 每个NALU包前面都有（lengthSizeMinusOne & 3）+ 1个字节的NAL包长度描述,这几个字节在打包成H264的时候,是不需要的.
func rtmpVideoPacketSplitNaluAndAppendAudSPSPPS(payload []byte, avc *avformat.AVCDecoderConfigurationRecord, naluSize uint32) (naluArray []byte, err error) {
	// 1 byte -> FrameType(4) + CodecID (4)
	// 2 byte -> AVCPacketType(8)
	// 3-5 byte -> CompositionTime(32)
	//frameType := (video.Payload[0] & 0xF0) >> 4
	//codecID := video.Payload[0] & 0x0F
	//avcPacketType := video.Payload[1]
	//compositionTime := video.Payload[2:5]

	// 在看srs产生的ts文件中,发现,只有pcr存在的那一帧(也即是I帧),会有(sps + pps).
	// 其他不是I帧的情况下,只有sei.
	// 但是无论是什么帧类型,都是在PES头之后,紧跟 00 00 00 01 09 F0.有以下两种情况(I帧,非I帧)
	// I frame: 00 00 00 01 09 f0 00 00 01 sei 00 00 01 sps 00 00 01 pps 00 00 01 i frame
	// Not I frame : 00 00 00 01 09 f0 00 00 01 sei 00 00 01 p frame

	var aud_sent bool
	var sps_pps_sent bool
	var prevIndex, length uint32

	prevIndex = 5

	for {

		if prevIndex == uint32(len(payload)) {
			break
		}

		if prevIndex+naluSize > uint32(len(payload)) {
			return nil, errors.New("rtmpVideoPacketAppendNaluAUD error 1!")
		}

		// nalu == nalu_length + nalu_data
		// nalu size : AVCDecoderConfigurationRecord.LengthSizeMinusOne + 1(即nalu length本身所占的字节数)
		// nalu length : 每个nalu长度
		// nalu data : 紧跟nalu length后面的负载数据
		nalu_length := payload[prevIndex : prevIndex+naluSize]

		// TODO: 如果长度大于4个字节呢？
		length, err = util.ByteToUint32N(nalu_length)
		if err != nil {
			return
		}

		if prevIndex+naluSize+length > uint32(len(payload)) {
			return nil, errors.New("rtmpVideoPacketAppendNaluAUD error 2!")
		}

		nalu_data := payload[prevIndex+naluSize : prevIndex+naluSize+length]

		nalu_type, err := util.ByteToUint32N(nalu_data[0:1])
		if err != nil {
			return nil, errors.New("rtmpVideoPacketSplitNaluAUD ByteToUint32N error")
		}

		nalu_type &= 0x1f

		// I frame or P freame
		// if nalu_type == 5 {
		// 	fmt.Println("I I I I I I I I I I I I I I I I I I I I I I I I I I")
		// }

		// if nalu_type == 1 {
		// 	fmt.Println("P P P P P P P P P P P P P P P P P P P P P P P P P P")
		// }

		// 7-9, ignore, @see: ngx_rtmp_hls_video
		if nalu_type >= 7 && nalu_type <= 9 {
			prevIndex = prevIndex + naluSize + length
			continue
		}

		// 一帧数据只会Append一个NALU_AUD
		if !aud_sent {
			// srs nginx 就是1,5,6都会打上NALU_AUD
			if nalu_type == 1 || nalu_type == 5 || nalu_type == 6 {
				naluArray = append(naluArray, avformat.NALU_AUD_BYTE...)
				aud_sent = true
			}
		}

		// sps pps append 00 00 00 01,只有是IDR Frame才会打上sps和pps,并且一帧只会打一次
		if nalu_type == 5 && !sps_pps_sent {
			sps_pps_sent = true

			if avc.SequenceParameterSetLength > 0 {
				naluArray = append(naluArray, avformat.NALU_Delimiter2...)
				naluArray = append(naluArray, avc.SequenceParameterSetNALUnit...)
			}

			if avc.PictureParameterSetLength > 0 {
				naluArray = append(naluArray, avformat.NALU_Delimiter2...)
				naluArray = append(naluArray, avc.PictureParameterSetNALUnit...)
			}
		}

		// @see: ngx_rtmp_hls_video, AnnexB prefix
		if len(nalu_data) < 5 {
			return nil, errors.New("hls: not enough buffer for AnnexB prefix")
		}

		// i,p,b frame, append 00 00 01
		naluArray = append(naluArray, avformat.NALU_Delimiter1...)
		naluArray = append(naluArray, nalu_data...)

		prevIndex = prevIndex + naluSize + length
	}

	return
}
