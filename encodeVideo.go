package hls

import (
	"bytes"

	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/codec/mpegts"
	"m7s.live/engine/v4/track"
)

func VideoPacketToPES(frame *VideoFrame, vt *track.Video) (packet mpegts.MpegTsPESPacket, err error) {
	buffer := bytes.NewBuffer([]byte{})
	//需要对原始数据(ES),进行一些预处理,视频需要分割nalu(H264编码),并且打上sps,pps,nalu_aud信息.
	if len(vt.ParamaterSets) == 2 {
		buffer.Write(codec.NALU_AUD_BYTE)
	} else {
		buffer.Write(codec.AudNalu)
	}
	if frame.IFrame {
		annexB := vt.GetAnnexB()
		annexB.WriteTo(buffer)
	}
	annexB := frame.GetAnnexB()
	annexB.WriteTo(buffer)

	pktLength := buffer.Len() + 10 + 3
	if pktLength > 0xffff {
		pktLength = 0
	}

	// cts = (pts - dts) / 90
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
	packet.Header.Pts = uint64(frame.PTS)
	packet.Header.Dts = uint64(frame.DTS)
	packet.Header.PtsDtsFlags = 0xC0
	packet.Header.PesHeaderDataLength = 10

	packet.Payload = buffer.Bytes()

	return
}
