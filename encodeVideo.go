package hls

import (
	"bytes"
	"os"

	. "github.com/Monibuca/engine/v3"
	"github.com/Monibuca/utils/v3/codec"
	"github.com/Monibuca/utils/v3/codec/mpegts"
)

func VideoPacketToPES(pack VideoPack, sps, pps []byte) (packet mpegts.MpegTsPESPacket, err error) {
	buffer := bytes.NewBuffer([]byte{})
	ts := pack.Timestamp
	//需要对原始数据(ES),进行一些预处理,视频需要分割nalu(H264编码),并且打上sps,pps,nalu_aud信息.
	for _, nalu := range pack.NALUs {
		switch nalu[0] & 0x1F {
		case codec.NALU_Non_IDR_Picture, codec.NALU_SEI:
			buffer.Write(codec.NALU_AUD_BYTE)
		case codec.NALU_IDR_Picture:
			buffer.Write(codec.NALU_AUD_BYTE)
			buffer.Write(codec.NALU_Delimiter2)
			buffer.Write(sps)
			buffer.Write(codec.NALU_Delimiter2)
			buffer.Write(pps)
		}
		buffer.Write(codec.NALU_Delimiter1)
		buffer.Write(nalu)
	}
	pktLength := buffer.Len() + 10 + 3
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

	packet.Payload = buffer.Bytes()

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
