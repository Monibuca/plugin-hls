package hls

import (

	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bluenviron/gohlslib"
	"github.com/bluenviron/gohlslib/pkg/codecs"
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/config"
	"m7s.live/engine/v4/track"
	"m7s.live/engine/v4/util"
)



var llhlsConfig = &LLHLSConfig{
	DefaultYaml: defaultYaml,
}
var LLHLSPlugin = InstallPlugin(llhlsConfig)
var llwriting util.Map[string, *LLMuxer]

type LLHLSConfig struct {
	DefaultYaml
	config.HTTP
	config.Publish
	// config.Pull
	config.Subscribe
	Filter string // 过滤，正则表达式
	Path   string
}

func (c *LLHLSConfig) OnEvent(event any) {
	switch v := event.(type) {
	case SEpublish:
		if !llwriting.Has(v.Target.Path) {
			var outStream LLMuxer
			llwriting.Set(v.Target.Path, &outStream)
			go outStream.Start(v.Target)
		}
	}
}

func (c *LLHLSConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	streamPath := strings.TrimPrefix(r.URL.Path, "/")
	streamPath = path.Dir(streamPath)
	if llwriting.Has(streamPath) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/"+streamPath)
		llwriting.Get(streamPath).Handle(w, r)
		return
	} else {
		w.Write([]byte(`<html><body><video src="/llhls/live/test/index.m3u8"></video></body></html>`))
	}
}

type LLVideoTrack struct {
	*track.AVRingReader
	*track.Video
}

type LLAudioTrack struct {
	*track.AVRingReader
	*track.Audio
}

type LLMuxer struct {
	*gohlslib.Muxer
	Subscriber
	audio_tracks []*LLAudioTrack
	video_tracks []*LLVideoTrack
}

func (ll *LLMuxer) OnEvent(event any) {
	var err error
	defer func() {
		if err != nil {
			ll.Warn("write stop", zap.Error(err))
			ll.Stop()
		}
	}()
	switch v := event.(type) {
	case *track.Video:
		// track := ll.CreateTrackReader(&v.Media)
		ll.video_tracks = append(ll.video_tracks, &LLVideoTrack{
			Video: v,
		})
	case *track.Audio:
		if v.CodecID != codec.CodecID_AAC {
			return
		}
		ll.audio_tracks = append(ll.audio_tracks, &LLAudioTrack{
			Audio: v,
		})
	default:
		ll.Subscriber.OnEvent(event)
	}
}

func (ll *LLMuxer) Start(s *Stream) {
	if err := HLSPlugin.Subscribe(s.Path, ll); err != nil {
		HLSPlugin.Error("LL-HLS Subscribe", zap.Error(err))
		return
	}
	ll.Muxer = &gohlslib.Muxer{
		Variant: gohlslib.MuxerVariantLowLatency,
		SegmentCount: func() int {
			return 7
		}(),
		SegmentDuration: 1 * time.Second,
	}
	var defaultAudio *LLAudioTrack
	var defaultVideo *LLVideoTrack
	for _, t := range ll.video_tracks {
		if defaultVideo == nil {
			defaultVideo = t
			t.AVRingReader = ll.CreateTrackReader(&t.Video.Media)
			t.Ring = t.IDRing
			ll.Muxer.VideoTrack = &gohlslib.Track{}
			switch t.Video.CodecID {
			case codec.CodecID_H264:
				ll.Muxer.VideoTrack.Codec = &codecs.H264{
					SPS: t.Video.SPS,
					PPS: t.Video.PPS,
				}
			case codec.CodecID_H265:
				ll.Muxer.VideoTrack.Codec = &codecs.H265{
					SPS: t.Video.SPS,
					PPS: t.Video.PPS,
					VPS: t.Video.ParamaterSets[2],
				}
			}
		}
	}
	for _, t := range ll.audio_tracks {
		if defaultAudio == nil {
			defaultAudio = t
			t.AVRingReader = ll.CreateTrackReader(&t.Audio.Media)
			if defaultVideo != nil {
				for t.IDRing == nil && !ll.IsClosed() {
					time.Sleep(time.Millisecond * 10)
				}
				t.Ring = t.IDRing
			} else {
				t.Ring = t.Audio.Ring
			}
			ll.Muxer.AudioTrack = &gohlslib.Track{
				Codec: &codecs.MPEG4Audio{
					Config: mpeg4audio.Config{
						Type:         2,
						SampleRate:   44100,
						ChannelCount: 2,
					},
				},
			}
		}
	}
	ll.Muxer.Start()
	defer ll.Muxer.Close()
	now := time.Now()
	for ll.IO.Err() == nil {
		for defaultAudio != nil {
			frame := defaultAudio.TryRead()
			if frame == nil {
				break
			}
			audioFrame := AudioFrame{
				AVFrame: frame,
			}
			ll.Muxer.WriteAudio(now.Add(frame.Timestamp-time.Second), frame.Timestamp, util.ConcatBuffers(audioFrame.GetADTS()))
			defaultAudio.MoveNext()
		}
		for defaultVideo != nil {
			frame := defaultVideo.TryRead()
			if frame == nil {
				break
			}
			var aus net.Buffers
			if frame.IFrame {
				aus = append(aus, defaultVideo.ParamaterSets...)
			}
			frame.AUList.Range(func(au *util.BLL) bool {
				aus = append(aus, util.ConcatBuffers(au.ToBuffers()))
				return true
			})
			ll.Muxer.WriteH26x(now.Add(frame.Timestamp-time.Second), frame.Timestamp, aus)
			defaultVideo.MoveNext()
		}
		time.Sleep(time.Millisecond * 10)
	}
}
