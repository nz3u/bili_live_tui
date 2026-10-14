# BILILIVE_TUI
一个用于黑听B站直播的tui客户端
![示例图](./img/bililive1.png)

# 功能
- 查看弹幕、发送弹幕
- 友好的登录方式
- 彩色弹幕显示
- 自适应终端大小
- 自动播放当前直播间音频，Alt+M 静音/取消静音

# 如何使用
## 配置文件
按 F2 在程序内编辑配置，Ctrl+S 保存，Esc 取消。支持默认直播间、直播间列表、缓存条数、显示选项和 User-Agent。保存时默认直播间若不在列表中，自动加入首位并写入配置；已在列表中则保留原顺序。当前直播间若被移出保存后的列表，退出设置后自动连接默认房间，连接成功才切换弹幕和音频；失败保留原房间、草稿及音频并显示切换失败，已经保存的配置不回滚。当前房间仍在列表时不会因修改默认房间而立即切房。列表和显示选项保存后生效，User-Agent 下次连接生效。
保存保留未知配置和注释；如果文件被外部修改，会拒绝覆盖，请重启后再修改。
```toml
room_id = 7777 # 默认直播间ID
room_ids = [7777, 3] # Alt+N 循环切换；默认房间缺席时自动加入首位
chat_buffer = 200 # 可以回滚多少条弹幕
```

## 直播音频
进入房间后自动播放，Alt+N 成功切房后替换音频；切房失败保留原音频。Alt+M 在弹幕区、输入区和设置界面均可静音/取消静音，不修改草稿，也不暂停直播进度。静音状态在切房及重连时保留，本次启动有效。

播放依赖 [mpv](https://mpv.io/installation/)，后台调用独立的本地播放器，不内嵌 mpv/FFmpeg，不依赖浏览器，也不弹出视频窗口。程序优先查找自身目录及其中的 `mpv` 子目录，再查找 PATH。已有本机 mpv 时，只需把它所在目录加入 PATH，无需随主程序携带便携播放器目录；正式及测试发布包均未打包 mpv，本机测试用播放器不是主程序的一部分。未安装时在原输入行提示，弹幕功能照常可用；安装后按 Alt+M 或等待自动重试。

- Linux：`sudo apt install mpv`；macOS：`brew install mpv`。
- Windows：可从 [mpv Windows 构建](https://github.com/shinchiro/mpv-winbuild-cmake/releases) 下载并解压到程序旁的 `mpv` 子目录，或将播放器加入 PATH。当前本机构建的 [播放器](<dist/mpv/mpv.exe>) 已准备好。
- 部分 Windows 便携构建不带 CA 证书。可从 [curl 的 Mozilla CA 证书包](https://curl.se/docs/caextract.html) 下载证书包，保存在播放器旁，名称如下（本机已放置 [证书包](<dist/mpv/cacert.pem>)）：
  ```text
  cacert.pem
  ```
  程序会自动使用它，始终保留 HTTPS 证书校验，不通过关闭校验绕过网络问题。

未开播、断流或网络错误会自动重试（一次失败周期后等待 15 秒）；收到开播/下播通知会更新音频连接。音频完整提示（含静音切换、未找到 mpv、连接失败）与保存设置一样只显示 1 秒；正常播放时只显示 `▶`，持续静音或未播放时不显示。提示复用原输入行，不增加常驻行，也不会清除保存/发送提示和草稿。

正常退出会取消请求、终止播放器并清理 IPC；Windows 10+ 在创建播放器时原子加入独立的 kill-on-close Job Object，关闭终端或强制结束主程序时也由系统终止播放器及其子进程，不依赖 Go 的退出清理。不会清理其他程序启动的播放器。

接口采用 `getRoomPlayInfo`，优先低清 AVC/FLV，失败尝试备用 CDN 或 HLS；每次重新获取带时效签名的地址，不写入配置和日志。参考 [Streamlink 的 B 站实现](https://raw.githubusercontent.com/streamlink/streamlink/master/src/streamlink/plugins/bilibili.py) 和 [mpv JSON IPC](https://mpv.io/manual/stable/#json-ipc)。只解码音轨不代表 CDN 只传音频：普通直播流仍可能下载音视频复用数据，带宽取决于平台返回的最低画质。

## 启动软件
### `linux`或`macos`
直接打开二进制文件即可
### `windows`
由于终端编码的支持，需要用以下参数启动
```shell
PowerShell -Command "& {[Console]::OutputEncoding = [Text.UTF8Encoding]::UTF8};.\bililive.exe"
```
本地构建目录附带 `start.bat`；也可在 Windows Terminal 中先执行 `chcp 65001` 再运行 `bililive.exe`。请在包含配置文件的目录中启动。

## 登录
直接扫描二维码即可。
由于在部分终端下，二维码无法正常显示，所以同时将二维码保存为文件`login.png`,扫描该文件也可完成登录

## 操作方式
按`tab`切换区域，在上方弹幕区域可以用上下左右或者类vim的方式或这直接鼠标滚轮移动
在下方区域则可以输入弹幕按回车发送。发送失败保留草稿，网络结果不明时请先确认是否已经发送，程序不会自动重发。

- `Alt+N`：切换到列表中的下一个直播间，分别保留已收消息及草稿。发送中或切换中暂不重复切换；切换失败保留原房间。离开的房间不会继续收消息。
- `Alt+M`：静音/取消静音当前音频，不暂停直播；切房时保持静音状态。
- `F2`：打开设置；`Tab` / `↑↓` 选择字段，布尔选项用空格切换；`Ctrl+S` 保存，`Esc` 取消。
- `F6`：在主界面用系统默认浏览器打开当前直播间（输入弹幕时也可用，不改变草稿）。切房尚未完成时打开原房间；Linux 需要 `xdg-open` 和桌面浏览器。
- 快捷键和状态复用原输入行，不增加常驻提示行。

## 开发验证
```shell
go test ./... -count=1 -timeout=120s
go vet ./...
go build -o dist/bililive.exe ./cmd/tui
```
需要更小的主程序时，可以去掉调试符号（无需更改播放器调用方式）：
```shell
go build -trimpath -ldflags="-s -w" -o dist/bililive.exe ./cmd/tui
```
默认测试使用本地模拟服务，不登录、播放或发送真实弹幕。音频测试覆盖开播重试、CDN 回退、切房、静音、过期结果和退出清理。真实登录、风控、拔网恢复和 Windows 终端 Alt 键行为需手工验收。

可显式启用真实音频测试（PowerShell；不登录或发送弹幕，启动时静音，短暂验证取消静音后恢复）：
```powershell
$env:BILILIVE_AUDIO_LIVE_TEST = '1'
$env:BILILIVE_MPV = (Resolve-Path .\dist\mpv\mpv.exe).Path
$env:BILILIVE_TEST_ROOM = '7777' # 必须是正在直播的房间
go test ./internal/tui -run '^TestMPVLiveAudio$' -v -count=1 -timeout=90s
Remove-Item Env:BILILIVE_AUDIO_LIVE_TEST, Env:BILILIVE_MPV, Env:BILILIVE_TEST_ROOM
```
已在本机真实验证 7777：48 kHz 双声道音频输出，视频解码关闭，静音时进度继续推进，IPC 静音/取消静音均成功。

Windows 下还可用本地静音 WAV 验证真实播放器随父进程退出（无需访问直播接口）：
```powershell
$env:BILILIVE_MPV = (Resolve-Path .\dist\mpv\mpv.exe).Path
go test ./internal/tui -run '^TestAudioWindowsRealMPVParentExit$' -v -count=1 -timeout=120s
Remove-Item Env:BILILIVE_MPV
```
测试分别验证正常退出及强制结束父进程，持有准确的播放器进程句柄检查其已终止，不以 PID 消失或复用作判断。

# 计划实现的功能
- 显示高能榜
- 显示礼物、进场、SC等
- 赠送礼物
- 自动守塔
- 支持充值（真DD）
- 统计直播的数据