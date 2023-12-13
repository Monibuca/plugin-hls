package hls // import "m7s.live/plugin/hls/v4"

import (
	"embed"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/config"
	"m7s.live/engine/v4/util"
)

//go:embed hls.js
var hls_js embed.FS

//go:embed default.ts
var defaultTS []byte

//go:embed default.yaml
var defaultYaml DefaultYaml
var defaultSeq = 0                        // 默认片头的全局序号
var writing = make(map[string]*HLSWriter) // preload 使用
var writingMap sync.Map                   // 非preload使用
var hlsConfig = &HLSConfig{}
var HLSPlugin = InstallPlugin(hlsConfig, defaultYaml)

type HLSConfig struct {
	config.HTTP
	config.Publish
	config.Pull
	config.Subscribe
	Fragment          time.Duration `default:"2s" desc:"ts分片大小"`
	Window            int           `default:"3" desc:"m3u8窗口大小(包含ts的数量)"`
	Filter            config.Regexp `desc:"用于过滤的正则表达式"` // 过滤，正则表达式
	Path              string        `desc:"保存 ts 文件的路径"`
	DefaultTS         string        `desc:"默认的ts文件"`                                     // 默认的ts文件
	DefaultTSDuration time.Duration `desc:"默认的ts文件时长"`                                   // 默认的ts文件时长
	RelayMode         int           `desc:"转发模式（转协议会消耗资源）" enum:"0:只转协议,1:纯转发,2:转协议+转发"` // 转发模式,0:转协议+不转发,1:不转协议+转发，2:转协议+转发
	Preload           bool          `desc:"是否预加载，提高响应速度"`                                // 是否预加载，提高响应速度
}

func (c *HLSConfig) OnEvent(event any) {
	switch v := event.(type) {
	case FirstConfig:
		if !c.Preload {
			c.Internal = false // 如何不预加载，则为非内部订阅
		}
		for streamPath, url := range c.PullOnStart {
			if err := HLSPlugin.Pull(streamPath, url, new(HLSPuller), 0); err != nil {
				HLSPlugin.Error("pull", zap.String("streamPath", streamPath), zap.String("url", url), zap.Error(err))
			}
		}
		if c.DefaultTS != "" {
			ts, err := os.ReadFile(c.DefaultTS)
			if err == nil {
				defaultTS = ts
			} else {
				log.Panic("read default ts error")
			}
		} else {
			c.DefaultTSDuration = time.Second * 388 / 100
		}
		if c.DefaultTSDuration == 0 {
			log.Panic("default ts duration error")
		} else {
			go func() {
				ticker := time.NewTicker(c.DefaultTSDuration)
				for range ticker.C {
					defaultSeq++
				}
			}()
		}
	case SEclose:
		if c.Preload {
			delete(writing, v.Target.Path)
		} else {
			writingMap.Delete(v.Target.Path)
		}
	case SEpublish:
		if c.Preload {
			if writing[v.Target.Path] == nil && (!c.Filter.Valid() || c.Filter.MatchString(v.Target.Path)) {
				if _, ok := v.Target.Publisher.(*HLSPuller); !ok || c.RelayMode == 0 {
					var outStream HLSWriter
					writing[v.Target.Path] = &outStream
					go outStream.Start(v.Target.Path)
				}
			}
		}
	case InvitePublish: //按需拉流
		if remoteURL := c.CheckPullOnSub(v.Target); remoteURL != "" {
			if err := HLSPlugin.Pull(v.Target, remoteURL, new(HLSPuller), 0); err != nil {
				HLSPlugin.Error("pull", zap.String("streamPath", v.Target), zap.String("url", remoteURL), zap.Error(err))
			}
		}
	}
}

func (config *HLSConfig) API_List(w http.ResponseWriter, r *http.Request) {
	util.ReturnFetchValue(FilterStreams[*HLSPuller], w, r)
}

// 处于拉流时，可以调用这个API将拉流的TS文件保存下来，这个http如果断开，则停止保存
func (config *HLSConfig) API_Save(w http.ResponseWriter, r *http.Request) {
	streamPath := r.URL.Query().Get("streamPath")
	if s := Streams.Get(streamPath); s != nil {
		if hls, ok := s.Publisher.(*HLSPuller); ok {
			hls.SaveContext = r.Context()
			<-hls.SaveContext.Done()
		}
	}
}

func (config *HLSConfig) API_Pull(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL.Query().Get("target")
	streamPath := r.URL.Query().Get("streamPath")
	save, _ := strconv.Atoi(r.URL.Query().Get("save"))
	if err := HLSPlugin.Pull(streamPath, targetURL, new(HLSPuller), save); err != nil {
		util.ReturnError(util.APIErrorQueryParse, err.Error(), w, r)
	} else {
		util.ReturnOK(w, r)
	}
}

func (config *HLSConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fileName := strings.TrimPrefix(r.URL.Path, "/")
	query := r.URL.Query()
	waitTimeout, err := time.ParseDuration(query.Get("timeout"))
	if err == nil {
		HLSPlugin.Info("wait timeout", zap.String("fileName", fileName), zap.Duration("timeout", waitTimeout))
	} else if !config.Preload {
		waitTimeout = time.Second * 10
	}
	waitStart := time.Now()
	if strings.HasSuffix(r.URL.Path, ".m3u8") {
		w.Header().Add("Content-Type", "application/vnd.apple.mpegurl")
		for {
			if v, ok := memoryM3u8.Load(strings.TrimSuffix(fileName, ".m3u8")); ok {
				switch hls := v.(type) {
				case *TrackReader:
					hls.RLock()
					w.Write(hls.M3u8)
					hls.RUnlock()
					return
				case string:
					fmt.Fprint(w, strings.Replace(hls, "?sub=1", util.Conditoinal(waitTimeout > 0, fmt.Sprintf("?sub=1&timeout=%s", waitTimeout), ""), -1))
					return
					// 				if _, ok := memoryM3u8.Load(hls); ok {
					// 					ss := strings.Split(hls, "/")
					// 					m3u8 := fmt.Sprintf(`#EXTM3U
					// #EXT-X-VERSION:3
					// #EXT-X-STREAM-INF:BANDWIDTH=2560000
					// %s/%s.m3u8
					// 					`, ss[len(ss)-2], ss[len(ss)-1])
					// 					w.Write([]byte(m3u8))
					// 					return
					// 				}
				}
			}
			if waitTimeout > 0 && time.Since(waitStart) < waitTimeout {
				if query.Get("sub") == "" {
					streamPath := strings.TrimSuffix(fileName, ".m3u8")
					if !config.Preload {
						writer, loaded := writingMap.LoadOrStore(streamPath, new(HLSWriter))
						if !loaded {
							outStream := writer.(*HLSWriter)
							go outStream.Start(streamPath + "?" + r.URL.RawQuery)
						}
					} else {
						TryInvitePublish(streamPath)
					}
				}
				time.Sleep(time.Second)
				continue
			} else {
				break
			}
		}

		w.Write([]byte(fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-MEDIA-SEQUENCE:%d
#EXT-X-TARGETDURATION:%d
#EXT-X-DISCONTINUITY-SEQUENCE:%d
#EXT-X-DISCONTINUITY
#EXTINF:%.3f,
default.ts`, defaultSeq, int(math.Ceil(config.DefaultTSDuration.Seconds())), defaultSeq, config.DefaultTSDuration.Seconds())))
	} else if strings.HasSuffix(r.URL.Path, ".ts") {
		w.Header().Add("Content-Type", "video/mp2t") //video/mp2t
		streamPath := path.Dir(fileName)
		for {
			tsData := memoryTs.Get(streamPath)
			if tsData == nil {
				tsData = memoryTs.Get(path.Dir(streamPath))
				if tsData == nil {
					if waitTimeout > 0 && time.Since(waitStart) < waitTimeout {
						time.Sleep(time.Second)
						continue
					} else {
						w.Write(defaultTS)
						return
					}
				}
			}
			for {
				if tsData := tsData.GetTs(fileName); tsData != nil {
					switch v := tsData.(type) {
					case *MemoryTs:
						v.WriteTo(w)
					case *util.ListItem[util.Buffer]:
						w.Write(v.Value)
					}
					return
				} else {
					if waitTimeout > 0 && time.Since(waitStart) < waitTimeout {
						time.Sleep(time.Second)
						continue
					} else {
						w.Write(defaultTS)
						return
					}
				}
			}
		}
	} else {
		f, err := hls_js.ReadFile("hls.js/" + fileName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			w.Write(f)
		}
		// if file, err := hls_js.Open(fileName); err == nil {
		// 	defer file.Close()
		// 	if info, err := file.Stat(); err == nil {
		// 		http.ServeContent(w, r, fileName, info.ModTime(), file)
		// 	}
		// } else {
		// 	http.NotFound(w, r)
		// }
	}
}
