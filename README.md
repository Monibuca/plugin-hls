# 介绍

> 该插件依赖github.com/Monibuca/plugin-ts

# 功能

1. 该插件可用来拉取网络上的m3u8文件并解析后转换成其他协议
2. 该插件可以在服务器写HLS文件，配合nginx等可以提供HLS的服务
3. 配合gateway插件可以直接访问http://localhost:8081/hls/live/user1.m3u8 进行播放，其中8081端口是gateway插件配置的，live/user1是streamPath，需要根据实际情况修改

# 配置

```toml
[HLS]
EnableWrite = false
Fragment = 5
Window = 2
Path = "hls"
```
EnableWrite 用来控制是否启用HLS文件写入功能

# 使用方法

1. 创建HLS结构体实例
2. 设置该对象的Video.Req和Audio.Req属性（用于请求m3u8文件）没有audio单独的m3u8可不设置Audio.Req属性
3. 调用对象的Publish方法，传入流名称和上级publisher，如果没有就传本对象
4. 使用UI界面，点击拉流转发，可以将一个远程的hls流拉入服务器中，配置StreamPath用于从服务器中播放这个流。

> 如果拉取远程ts文件需要cookie或者http的验证，可以将验证头设置到HLS对象的TsHead属性中。

```go
import 	. "github.com/Monibuca/plugin-ts"
func demo(){
    p:=new(HLS)
    p.Video.Req = http.NewRequest("GET","http://xxxx.com/demo.m3u8", nil)
    p.Publish("live/hls")
}
```