package live_room

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/shr-go/bili_live_tui/api"
)

const (
	maxFrame   = 4 << 20
	maxDecoded = 16 << 20
	maxDepth   = 4
	ioTimeout  = 10 * time.Second
)

var invalidMessageErr = errors.New("invalid message")

func packMessage(data []byte, proto api.DanmuProtol, op api.DanmuOp, seq uint32) []byte {
	var b bytes.Buffer
	switch proto {
	case api.DanmuProtolNormalZlib:
		w := zlib.NewWriter(&b)
		_, _ = w.Write(data)
		_ = w.Close()
		data = b.Bytes()
	case api.DanmuProtolNormalBrotli:
		w := brotli.NewWriter(&b)
		_, _ = w.Write(data)
		_ = w.Close()
		data = b.Bytes()
	}
	p := make([]byte, 16+len(data))
	binary.BigEndian.PutUint32(p, uint32(len(p)))
	binary.BigEndian.PutUint16(p[4:], 16)
	binary.BigEndian.PutUint16(p[6:], uint16(proto))
	binary.BigEndian.PutUint32(p[8:], uint32(op))
	binary.BigEndian.PutUint32(p[12:], seq)
	copy(p[16:], data)
	return p
}
func parseHeader(p []byte) (*api.DanmuMessageHeader, error) {
	if len(p) < 16 {
		return nil, io.ErrUnexpectedEOF
	}
	h := &api.DanmuMessageHeader{Size: binary.BigEndian.Uint32(p), HeaderSize: binary.BigEndian.Uint16(p[4:]), ProtoVer: api.DanmuProtol(binary.BigEndian.Uint16(p[6:])), OpCode: api.DanmuOp(binary.BigEndian.Uint32(p[8:])), Sequence: binary.BigEndian.Uint32(p[12:])}
	if h.HeaderSize != 16 || h.Size < 16 || h.Size > maxFrame || h.ProtoVer > 3 {
		return nil, invalidMessageErr
	}
	return h, nil
}
func readFrame(c net.Conn, timeout time.Duration) ([]byte, error) {
	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	p := make([]byte, 16)
	if _, err := io.ReadFull(c, p); err != nil {
		return nil, err
	}
	h, err := parseHeader(p)
	if err != nil {
		return nil, err
	}
	p = append(p, make([]byte, int(h.Size)-16)...)
	_, err = io.ReadFull(c, p[16:])
	return p, err
}
func writeFrame(c net.Conn, p []byte) error {
	if len(p) > maxFrame {
		return invalidMessageErr
	}
	if err := c.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	for len(p) > 0 {
		n, err := c.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func decodeMessages(ctx context.Context, room *api.LiveRoom, p []byte, depth int, budget *int) error {
	if depth > maxDepth {
		return invalidMessageErr
	}
	for len(p) > 0 {
		h, err := parseHeader(p)
		if err != nil {
			return err
		}
		if int(h.Size) > len(p) {
			return io.ErrUnexpectedEOF
		}
		body := p[16:h.Size]
		p = p[h.Size:]
		if h.ProtoVer == 2 || h.ProtoVer == 3 {
			var reader io.Reader
			if h.ProtoVer == 2 {
				z, err := zlib.NewReader(bytes.NewReader(body))
				if err != nil {
					return err
				}
				defer z.Close()
				reader = z
			} else {
				reader = brotli.NewReader(bytes.NewReader(body))
			}
			decoded, err := io.ReadAll(io.LimitReader(reader, int64(*budget)+1))
			if err != nil {
				return err
			}
			*budget -= len(decoded)
			if *budget < 0 {
				return invalidMessageErr
			}
			if err := decodeMessages(ctx, room, decoded, depth+1, budget); err != nil {
				return err
			}
			continue
		}
		switch h.OpCode {
		case api.DanmuOpHeartBeatResp:
			if len(body) != 4 {
				return invalidMessageErr
			}
			atomic.StoreUint32(&room.Hot, binary.BigEndian.Uint32(body))
		case api.DanmuOpNormal:
			m := new(api.DanmuMessage)
			if err := json.Unmarshal(body, m); err != nil {
				return err
			}
			select {
			case room.MessageChan <- m:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}
func unpackMessage(room *api.LiveRoom, p []byte) uint32 {
	budget := maxDecoded
	ctx := room.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if decodeMessages(ctx, room, p, 0, &budget) != nil {
		return 0
	}
	return uint32(len(p))
}
func GetDanmuInfo(client *http.Client, id uint64) (*api.DanmuInfoResp, error) {
	return getDanmuInfo(context.Background(), client, id)
}
func getDanmuInfo(ctx context.Context, client *http.Client, id uint64) (*api.DanmuInfoResp, error) {
	u, err := signURL(ctx, client, fmt.Sprintf("https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo?id=%d", id))
	if err != nil {
		return nil, err
	}
	var info api.DanmuInfoResp
	if err := getJSON(ctx, client, u, &info); err != nil {
		return nil, err
	}
	if info.Code != 0 {
		return nil, fmt.Errorf("danmu info: %s", info.Message)
	}
	return &info, nil
}
func connectDanmuServer(uid, id uint64, info *api.DanmuInfoResp) (net.Conn, error) {
	return connect(context.Background(), uid, id, info)
}
func connect(ctx context.Context, uid, id uint64, info *api.DanmuInfoResp) (net.Conn, error) {
	if info == nil {
		return nil, errors.New("missing danmu info")
	}
	last := errors.New("no available danmu servers")
	for _, host := range info.Data.HostList {
		c, err := (&net.Dialer{Timeout: ioTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(host.Host, strconv.Itoa(host.Port)))
		if err != nil {
			last = err
			continue
		}
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		payload, _ := json.Marshal(api.DanmuAuthPacketReq{UID: uid, RoomID: id, ProtoVer: 3, Platform: "web", Type: 2, Key: info.Data.Token})
		err = writeFrame(c, packMessage(payload, 1, api.DanmuOpAuth, 1))
		if err == nil {
			var p []byte
			p, err = readFrame(c, ioTimeout)
			if err == nil {
				h, _ := parseHeader(p)
				var auth struct {
					Code *int `json:"code"`
				}
				err = json.Unmarshal(p[16:], &auth)
				if err == nil && (h.OpCode != api.DanmuOpAuthResp || h.ProtoVer > 1 || auth.Code == nil || *auth.Code != 0) {
					err = errors.New("danmu authentication rejected")
				}
			}
		}
		stop()
		if err == nil && ctx.Err() == nil {
			return c, nil
		}
		_ = c.Close()
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, last
}

// ConnectDanmuServer is compatibility-only; AuthAndConnect also initializes metadata and credentials.
func ConnectDanmuServer(uid, id uint64, info *api.DanmuInfoResp) (*api.LiveRoom, error) {
	c, err := connectDanmuServer(uid, id, info)
	if err != nil {
		return nil, err
	}
	room := api.NewLiveRoom()
	room.UID = uid
	room.RoomID = id
	room.Client = http.DefaultClient
	go supervise(room, c)
	return room, nil
}
func generation(room *api.LiveRoom, c net.Conn) {
	ctx, cancel := context.WithCancel(room.Context())
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer cancel()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		heartbeat := func() error {
			return writeFrame(c, packMessage([]byte("[object Object]"), 1, api.DanmuOpHeartBeat, atomic.AddUint32(&room.Seq, 1)))
		}
		if heartbeat() != nil {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if heartbeat() != nil {
					return
				}
			case p, ok := <-room.ReqChan:
				if !ok || writeFrame(c, p) != nil {
					return
				}
			}
		}
	}()
	for {
		p, err := readFrame(c, 75*time.Second)
		if err != nil {
			break
		}
		budget := maxDecoded
		if decodeMessages(ctx, room, p, 0, &budget) != nil {
			break
		}
	}
	cancel()
	_ = c.Close()
	<-writerDone
	stop()
}
func supervise(room *api.LiveRoom, c net.Conn) {
	defer room.Finish()
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		if room.RoomUserInfo != nil {
			processHeartBeat(room)
		}
	}()
	defer func() { <-hbDone }()
	delay := 250 * time.Millisecond
	for {
		if c != nil {
			generation(room, c)
			c = nil
		}
		select {
		case <-room.DoneChan:
			return
		case <-time.After(delay):
		}
		info, err := getDanmuInfo(room.Context(), room.Client, room.RoomID)
		if err == nil {
			c, err = connect(room.Context(), room.UID, room.RoomID, info)
		}
		if err != nil {
			delay *= 2
			if delay > 8*time.Second {
				delay = 8 * time.Second
			}
		} else {
			delay = 250 * time.Millisecond
		}
	}
}
