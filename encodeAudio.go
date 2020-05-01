package hls

import (
	"errors"

	"github.com/Monibuca/engine/v2/avformat"
	"github.com/Monibuca/engine/v2/avformat/mpegts"
)

func rtmpAudioPacketToPESPreprocess(aacRaw []byte, aac_asc avformat.AudioSpecificConfig) (data []byte, err error) {
	// adts
	if _, data, err = avformat.AudioSpecificConfigToADTS(aac_asc, len(aacRaw)); err != nil {
		return
	}

	// adts + aac raw
	data = append(data, aacRaw...)
	return
}

func rtmpAudioPacketToPES(audio *avformat.SendPacket, aac_asc avformat.AudioSpecificConfig) (packet mpegts.MpegTsPESPacket, err error) {
	var data []byte

	if data, err = rtmpAudioPacketToPESPreprocess(audio.Payload[2:], aac_asc); err != nil {
		return
	}

	// packetLength = 原始音频流长度 + adts(7) + MpegTsOptionalPESHeader长度(8 bytes, 因为只含有pts)
	pktLength := len(data) + 8

	packet.Header.PacketStartCodePrefix = 0x000001
	packet.Header.ConstTen = 0x80
	packet.Header.StreamID = mpegts.STREAM_ID_AUDIO
	packet.Header.PesPacketLength = uint16(pktLength)
	packet.Header.Pts = uint64(audio.Timestamp) * 90
	packet.Header.PtsDtsFlags = 0x80
	packet.Header.PesHeaderDataLength = 5

	packet.Payload = data

	return
}

func decodeAudioSpecificConfig(audio *avformat.AVPacket) (asc avformat.AudioSpecificConfig, err error) {
	if len(audio.Payload) < 4 {
		err = errors.New("decodeAudioSpecificConfig error 1")
		return
	}

	// AACPacketType, 0 = AAC sequence header，1 = AAC raw
	if audio.Payload[1] != 0 {
		err = errors.New("decodeAudioSpecificConfig error : this packet is not AAC sequence header")
		return
	}

	// 前面有2个字节(音频信息)
	asc.AudioObjectType = (audio.Payload[2] & 0xF8) >> 3
	asc.SamplingFrequencyIndex = (audio.Payload[2] & 0x07 << 1) | (audio.Payload[3] >> 7)
	asc.ChannelConfiguration = (audio.Payload[3] >> 3) & 0x0F
	asc.FrameLengthFlag = (audio.Payload[3] >> 2) & 0x01
	asc.DependsOnCoreCoder = (audio.Payload[3] >> 1) & 0x01
	asc.ExtensionFlag = audio.Payload[3] & 0x01

	return
}
