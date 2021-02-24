package hls

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Monibuca/utils/v3/codec/mpegts"

	. "github.com/Monibuca/engine/v3"
	. "github.com/Monibuca/plugin-ts/v3"
	. "github.com/Monibuca/utils/v3"
	"github.com/quangngotan95/go-m3u8/m3u8"
)

var collection sync.Map
var config struct {
	Fragment     int64
	Window       int
	EnableWrite  bool   //启动HLS写文件
	EnableMemory bool   // 启动内存模式
	Path         string //存放路径
}

func init() {
	config.Fragment = 10
	config.Window = 2
	InstallPlugin(&PluginConfig{
		Name:   "HLS",
		Config: &config,
		Run: func() {
			//os.MkdirAll(config.Path, 0666)
			if config.EnableWrite || config.EnableMemory {
				AddHook(HOOK_PUBLISH, func(v interface{}) {
					writeHLS(v.(*Stream))
				})
			}
		},
	})
	http.HandleFunc("/api/hls/list", func(w http.ResponseWriter, r *http.Request) {
		sse := NewSSE(w, r.Context())
		var err error
		for tick := time.NewTicker(time.Second); err == nil; <-tick.C {
			var info []*HLS
			collection.Range(func(key, value interface{}) bool {
				info = append(info, value.(*HLS))
				return true
			})
			err = sse.WriteJSON(info)
		}
	})
	http.HandleFunc("/api/hls/save", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		streamPath := r.URL.Query().Get("streamPath")
		if data, ok := collection.Load(streamPath); ok {
			hls := data.(*HLS)
			hls.SaveContext = r.Context()
			<-hls.SaveContext.Done()
		}
	})
	http.HandleFunc("/api/hls/pull", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		targetURL := r.URL.Query().Get("target")
		streamPath := r.URL.Query().Get("streamPath")
		p := new(HLS)
		var err error
		p.Video.Req, err = http.NewRequest("GET", targetURL, nil)
		if err == nil {
			p.Publish(streamPath)
			w.Write([]byte(`{"code":0}`))
		} else {
			w.Write([]byte(fmt.Sprintf(`{"code":1,"msg":"%s"}`, err.Error())))
		}
	})
	http.HandleFunc("/hls/", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			if f, err := os.Open(filepath.Join(config.Path, strings.TrimPrefix(r.URL.Path, "/hls/"))); err == nil {
				io.Copy(w, f)
				err = f.Close()
			} else {
				w.WriteHeader(404)
			}
		} else if strings.HasSuffix(r.URL.Path, ".ts") {
			tsPath := filepath.Join(config.Path, strings.TrimPrefix(r.URL.Path, "/hls/"))
			if config.EnableMemory {
				if ringItem, ok := memoryTs.Load(tsPath); ok {
					w.Write(mpegts.DefaultPATPacket)
					w.Write(mpegts.DefaultPMTPacket)
					ringItem.(*RingItem_Video).Wait()
					w.Write(ringItem.(*RingItem_Video).Buffer.Bytes())
				} else {
					w.WriteHeader(404)
				}
			} else {
				if f, err := os.Open(tsPath); err == nil {
					io.Copy(w, f)
					err = f.Close()
				} else {
					w.WriteHeader(404)
				}
			}
		}
	})
}

// HLS 发布者
type HLS struct {
	TS
	Video       M3u8Info
	Audio       M3u8Info
	TsHead      http.Header     `json:"-"` //用于提供cookie等特殊身份的http头
	SaveContext context.Context `json:"-"` //用来保存ts文件到服务器
}

// M3u8Info m3u8文件的信息，用于拉取m3u8文件，和提供查询
type M3u8Info struct {
	Req       *http.Request `json:"-"`
	M3U8Count int           //一共拉取的m3u8文件数量
	TSCount   int           //一共拉取的ts文件数量
	LastM3u8  string        //最后一个m3u8文件内容
	M3u8Info  []TSCost      //每一个ts文件的消耗
}

// TSCost ts文件拉取的消耗信息
type TSCost struct {
	DownloadCost int
	DecodeCost   int
	BufferLength int
}

func readM3U8(res *http.Response) (playlist *m3u8.Playlist, err error) {
	var reader io.Reader = res.Body
	if res.Header.Get("Content-Encoding") == "gzip" {
		reader, err = gzip.NewReader(reader)
	}
	if err == nil {
		playlist, err = m3u8.Read(reader)
	}
	if err != nil {
		log.Printf("readM3U8 error:%s", err.Error())
	}
	return
}
func (p *HLS) run(info *M3u8Info) {
	//请求失败自动退出
	req := info.Req.WithContext(p)
	client := http.Client{Timeout: time.Second * 5}
	sequence := 0
	lastTs := make(map[string]bool)
	resp, err := client.Do(req)
	defer func() {
		log.Printf("hls %s exit:%v", p.StreamPath, err)
		p.Close()
	}()
	errcount := 0
	for ; err == nil; resp, err = client.Do(req) {
		if playlist, err := readM3U8(resp); err == nil {
			errcount = 0
			info.LastM3u8 = playlist.String()
			//if !playlist.Live {
			//	log.Println(p.LastM3u8)
			//	return
			//}
			if playlist.Sequence <= sequence {
				log.Printf("same sequence:%d,max:%d", playlist.Sequence, sequence)
				time.Sleep(time.Second)
				continue
			}
			info.M3U8Count++
			sequence = playlist.Sequence
			thisTs := make(map[string]bool)
			tsItems := make([]*m3u8.SegmentItem, 0)
			discontinuity := false
			for _, item := range playlist.Items {
				switch v := item.(type) {
				case *m3u8.DiscontinuityItem:
					discontinuity = true
				case *m3u8.SegmentItem:
					thisTs[v.Segment] = true
					if _, ok := lastTs[v.Segment]; ok && !discontinuity {
						continue
					}
					tsItems = append(tsItems, v)
				}
			}
			lastTs = thisTs
			if len(tsItems) > 3 {
				tsItems = tsItems[len(tsItems)-3:]
			}
			info.M3u8Info = nil
			for _, v := range tsItems {
				tsCost := TSCost{}
				tsUrl, _ := info.Req.URL.Parse(v.Segment)
				tsReq, _ := http.NewRequestWithContext(p, "GET", tsUrl.String(), nil)
				tsReq.Header = p.TsHead
				t1 := time.Now()
				if tsRes, err := client.Do(tsReq); err == nil {
					info.TSCount++
					if body, err := ioutil.ReadAll(tsRes.Body); err == nil {
						tsCost.DownloadCost = int(time.Since(t1) / time.Millisecond)
						if p.SaveContext != nil && p.SaveContext.Err() == nil {
							os.MkdirAll(filepath.Join(config.Path, p.StreamPath), 0666)
							err = ioutil.WriteFile(filepath.Join(config.Path, p.StreamPath, filepath.Base(tsUrl.Path)), body, 0666)
						}
						t1 = time.Now()
						beginLen := len(p.TsPesPktChan)
						if err = p.Feed(bytes.NewReader(body)); err != nil {
							//close(p.TsPesPktChan)
						} else {
							tsCost.DecodeCost = int(time.Since(t1) / time.Millisecond)
							tsCost.BufferLength = len(p.TsPesPktChan)
							p.PesCount = tsCost.BufferLength - beginLen
						}
					} else if err != nil {
						log.Printf("%s readTs:%v", p.StreamPath, err)
					}
				} else if err != nil {
					log.Printf("%s reqTs:%v", p.StreamPath, err)
				}
				info.M3u8Info = append(info.M3u8Info, tsCost)
			}

			time.Sleep(time.Second * time.Duration(playlist.Target) * 2)
		} else {
			log.Printf("%s readM3u8:%v", p.StreamPath, err)
			errcount++
			if errcount > 10 {
				return
			}
			//return
		}
	}
}

func (p *HLS) Publish(streamName string) (result bool) {
	if result = p.TS.Publish(streamName); result {
		p.Type = "HLS"
		collection.Store(streamName, p)
		go func() {
			p.run(&p.Video)
			collection.Delete(streamName)
		}()
		if p.Audio.Req != nil {
			go p.run(&p.Audio)
		}
	}
	return
}
