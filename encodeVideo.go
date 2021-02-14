package hls

import (
	"errors"
	"os"

	. "github.com/Monibuca/engine/v3"
	"github.com/Monibuca/utils/v3"
	"github.com/Monibuca/utils/v3/codec"
	"github.com/Monibuca/utils/v3/codec/mpegts"
)

func decodeAVCDecoderConfigurationRecord(video []byte) (avc_dcr codec.AVCDecoderConfigurationRecord, err error) {

	// 前面有5个字节(视频信息).
	avc_dcr.ConfigurationVersion = video[0]
	avc_dcr.AVCProfileIndication = video[1]
	avc_dcr.ProfileCompatibility = video[2]
	avc_dcr.AVCLevelIndication = video[3]
	avc_dcr.Reserved1 = video[4] >> 2            // reserved 111111
	avc_dcr.LengthSizeMinusOne = video[4] & 0x03 // H.264 视频中 NALU 的长度,一般为3
	avc_dcr.Reserved2 = video[5] >> 5            // reserved 111

	avc_dcr.NumOfSequenceParameterSets = video[5] & 31                     // sps个数,一般为1
	avc_dcr.SequenceParameterSetLength = utils.BigEndian.Uint16(video[6:]) // sps长度

	if len(video) < 9+int(avc_dcr.SequenceParameterSetLength)+2 {
		err = errors.New("decodeAVCDecoderConfigurationRecord error 2")
		return
	}
	avc_dcr.SequenceParameterSetNALUnit = video[8 : 8+int(avc_dcr.SequenceParameterSetLength)] // sps

	avc_dcr.NumOfPictureParameterSets = video[8+int(avc_dcr.SequenceParameterSetLength)]                          // pps个数,一般为1
	avc_dcr.PictureParameterSetLength = utils.BigEndian.Uint16(video[9+int(avc_dcr.SequenceParameterSetLength):]) // pps长度
	avc_dcr.PictureParameterSetNALUnit = video[9+int(avc_dcr.SequenceParameterSetLength)+2:]                      // pps

	return
}
func VideoPacketToPES(pack VideoPack, avc_dcr codec.AVCDecoderConfigurationRecord) (packet mpegts.MpegTsPESPacket, err error) {
	var data []byte
	ts := pack.Timestamp
	//需要对原始数据(ES),进行一些预处理,视频需要分割nalu(H264编码),并且打上sps,pps,nalu_aud信息.
	
	switch pack.NalType {
	case codec.NALU_Non_IDR_Picture, codec.NALU_IDR_Picture, codec.NALU_SEI:
		data = append(data, codec.NALU_AUD_BYTE...)
	}
	switch pack.NalType {
	case codec.NALU_IDR_Picture:
		if avc_dcr.SequenceParameterSetLength > 0 {
			data = append(data, codec.NALU_Delimiter2...)
			data = append(data, avc_dcr.SequenceParameterSetNALUnit...)
		}

		if avc_dcr.PictureParameterSetLength > 0 {
			data = append(data, codec.NALU_Delimiter2...)
			data = append(data, avc_dcr.PictureParameterSetNALUnit...)
		}
	default:
		data = append(data, codec.NALU_Delimiter1...)
		data = append(data, pack.Payload...)
	}
	pktLength := len(data) + 10 + 3
	if pktLength > 0xffff {
		pktLength = 0
	}

	// cts = (pts - dts) / 90
	var cts uint32
	// var avcPktType uint32
	// if avcPktType, err = utils.ByteToUint32N(payload[1:2]); err != nil {
	// 	return
	// }

	// if avcPktType == 1 {
	// 	if cts, err = utils.ByteToUint32N(payload[2:5]); err != nil {
	// 		return
	// 	}
	// }

	//cts = ((cts & 0x00FF0000) >> 16) | ((cts & 0x000000FF) << 16) | (cts & 0x0000FF00)

	packet.Header.PacketStartCodePrefix = 0x000001
	packet.Header.ConstTen = 0x80
	packet.Header.StreamID = mpegts.STREAM_ID_VIDEO
	packet.Header.PesPacketLength = uint16(pktLength)
	packet.Header.Pts = uint64(ts+cts) * 90
	packet.Header.Dts = uint64(ts) * 90
	packet.Header.PtsDtsFlags = 0xC0
	packet.Header.PesHeaderDataLength = 10

	packet.Payload = data

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