# HLS插件

- 该插件可用来拉取网络上的m3u8文件并解析后转换成其他协议
- 可以直接访问`http://localhost:8080/hls/live/user1.m3u8` 进行播放，其中8080端口是全局HTTP配置，live/user1是streamPath，需要根据实际情况修改

## 插件地址

https://github.com/Monibuca/plugin-hls

## 插件引入
```go
import (
    _ "m7s.live/plugin/hls/v4"
)
```

## API
> 参数是可变的，下面的参数live/hls是作为例子，不是固定的
- `/hls/api/list`
列出所有HLS流，是一个SSE，可以持续接受到列表数据，加上?json=1 可以返回json数据。
- `/hls/api/save?streamPath=live/hls`
保存指定的流（例如live/hls）为HLS文件（m3u8和ts）当这个请求关闭时就结束保存（该API仅作用于远程拉流）
- `/hls/api/pull?streamPath=live/hls&target=http://localhost/abc.m3u8`
将目标HLS流拉过来作为媒体源在monibuca内以`live/hls`流的形式存在
## 配置
- 配置信息按照需要添加到配置文件中，无需复制全部默认配置信息
- publish 和 subscribe 配置会覆盖全局配置
```yaml
hls:
    publish:
        pubaudio: true
        pubvideo: true
        kickexist: false
        publishtimeout: 10
        waitclosetimeout: 0
        delayclosetimeout: 0
    pull:
        repull: 0
        pullonstart: {} # 服务启动时自动拉流
        pullonsub: {} # 订阅时自动拉流(按需拉流)
    subscribe:
        subaudio: true
        subvideo: true
        iframeonly: false
        waittimeout: 10
    fragment: 10 # TS分片长度，单位秒
    window: 2 # 实时流m3u8文件包含的TS文件数
    filter: "" # 正则表达式，用来过滤发布的流，只有匹配到的流才会写入
    path: "" # 远端拉流如果需要保存的话，存放的目录
    defaultts: "" # 默认切片用于无流时片头播放,如果留空则使用系统内置
    defaulttsduration: 3.88 # 默认切片的长度（秒）
```