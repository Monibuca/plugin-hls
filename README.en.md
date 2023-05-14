# HLS plugin

- This plugin can be used to pull m3u8 files on the network and parse them into other protocols after parsing
- You can directly access `http://localhost:8080/hls/live/user1.m3u8` for playback, where port 8080 is the global HTTP configuration, live/user1 is streamPath, which needs to be modified according to the actual situation

## Plugin address

https://github.com/Monibuca/plugin-hls

## Plugin introduction

```go
import (
    _ "m7s.live/plugin/hls/v4"
)
```

## API
> The parameters are variable. The following parameters live/hls are used as examples, not fixed
- `/hls/api/list`
List all HLS streams, is an SSE, can continue to receive the list data, plus?json=1 can return json data.
- `/hls/api/save?streamPath=live/hls`
Save the specified stream (such as live/hls) as an HLS file (m3u8 and ts) when this request is closed, the save ends (this API only works for remote pulling)
- `/hls/api/pull?streamPath=live/hls&target=http://localhost/abc.m3u8`
Pull the target HLS stream over as a media source in monibuca in the form of `live/hls` stream

## Configuration
- The configuration information is added to the configuration file as needed, and there is no need to copy all the default configuration information
- The publish and subscribe configurations will override the global configuration
```yaml
hls:
    publish: # Format reference global configuration
    subscribe: # Format reference global configuration
    pull: # Format https://m7s.live/guide/config.html#%E6%8F%92%E4%BB%B6%E9%85%8D%E7%BD%AE
    fragment: 10s # TS fragment length
    window: 2 # The number of TS files included in the real-time stream m3u8 file
    filter: "" # Regular expression used to filter published streams, only streams that match will be written
    path: "" # If the remote stream needs to be saved, the directory where it is stored
    defaultts: "" # The default slice is used for the slice header playback when there is no stream. If it is empty, the system built-in is used
    defaulttsduration: 3.88s # The length of the default slice
    relaymode: 0 # Forwarding mode, 0: transfer protocol + no forwarding, 1: no transfer protocol + forwarding, 2: transfer protocol + forwarding
```

## Relay mode

The relay mode only works for hls that pulls streams from the remote end.

relaymode can be configured with different forwarding modes

Among them, protocol conversion means that HLS can pull streams and convert them to other protocol formats, which requires parsing of HLS data,

Forwarding means that the TS files in HLS are cached on the server and can be directly read when pulling streams from the server.

For example, if you want to only do pure forwarding for HLS and reduce CPU consumption, you can configure

```yaml
hls:
  relaymode: 1
```
