package hls

import (
	"errors"

	"github.com/Monibuca/utils/v3/codec"
	"github.com/Monibuca/utils/v3/codec/mpegts"
)

func AudioPacketToPESPreprocess(aacRaw []byte, aac_asc codec.AudioSpecificConfig) (data []byte, err error) {
	// adts
	if _, data, err = codec.AudioSpecificConfigToADTS(aac_asc, len(aacRaw)); err != nil {
		return
	}

	// adts + aac raw
	data = append(data, aacRaw...)
	return
}

func AudioPacketToPES(ts uint32 ,payload []byte, aac_asc codec.AudioSpecificConfig) (packet mpegts.MpegTsPESPacket, err error) {
	var data []byte

	if data, err = AudioPacketToPESPreprocess(payload, aac_asc); err != nil {
		return
	}

	// packetLength = 原始音频流长度 + adts(7) + MpegTsOptionalPESHeader长度(8 bytes, 因为只含有pts)
	pktLength := len(data) + 8

	packet.Header.PacketStartCodePrefix = 0x000001
	packet.Header.ConstTen = 0x80
	packet.Header.StreamID = mpegts.STREAM_ID_AUDIO
	packet.Header.PesPacketLength = uint16(pktLength)
	packet.Header.Pts = uint64(ts) * 90
	packet.Header.PtsDtsFlags = 0x80
	packet.Header.PesHeaderDataLength = 5

	packet.Payload = data

	return
}

func decodeAudioSpecificConfig(audio []byte) (asc codec.AudioSpecificConfig, err error) {
	if len(audio) < 4 {
		err = errors.New("decodeAudioSpecificConfig error 1")
		return
	}

	// AACPacketType, 0 = AAC sequence header，1 = AAC raw
	if audio[1] != 0 {
		err = errors.New("decodeAudioSpecificConfig error : this packet is not AAC sequence header")
		return
	}

	// 前面有2个字节(音频信息)
	asc.AudioObjectType = (audio[2] & 0xF8) >> 3
	asc.SamplingFrequencyIndex = (audio[2] & 0x07 << 1) | (audio[3] >> 7)
	asc.ChannelConfiguration = (audio[3] >> 3) & 0x0F
	asc.FrameLengthFlag = (audio[3] >> 2) & 0x01
	asc.DependsOnCoreCoder = (audio[3] >> 1) & 0x01
	asc.ExtensionFlag = audio[3] & 0x01

	return
}
