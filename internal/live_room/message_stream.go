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
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/google/go-querystring/query"
	"github.com/shr-go/bili_live_tui/api"
	"github.com/shr-go/bili_live_tui/pkg/logging"
)

var (
	invalidMessageErr    = errors.New("message invalid")
	headerNotCompleteErr = errors.New("header not complete")
)

const (
	maxPacketSize = 16 * 1024 * 1024
	authTimeout   = 10 * time.Second
	writeTimeout  = 10 * time.Second
	readTimeout   = 90 * time.Second // Three missed 30-second heartbeats.
)

func packMessage(data []byte, protoVer api.DanmuProtol, protoOp api.DanmuOp, seq uint32) []byte {
	var body bytes.Buffer
	switch protoVer {
	case api.DanmuProtolNormalZlib:
		w := zlib.NewWriter(&body)
		_, _ = w.Write(data)
		_ = w.Close()
	case api.DanmuProtolNormalBrotli:
		w := brotli.NewWriter(&body)
		_, _ = w.Write(data)
		_ = w.Close()
	default:
		body.Write(data)
	}
	packet := make([]byte, 16+body.Len())
	binary.BigEndian.PutUint32(packet[0:4], uint32(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], 16)
	binary.BigEndian.PutUint16(packet[6:8], uint16(protoVer))
	binary.BigEndian.PutUint32(packet[8:12], uint32(protoOp))
	binary.BigEndian.PutUint32(packet[12:16], seq)
	copy(packet[16:], body.Bytes())
	return packet
}

func parseHeader(data []byte) (*api.DanmuMessageHeader, error) {
	if len(data) < 16 {
		return nil, headerNotCompleteErr
	}
	header := &api.DanmuMessageHeader{
		Size:       binary.BigEndian.Uint32(data[0:4]),
		HeaderSize: binary.BigEndian.Uint16(data[4:6]),
		ProtoVer:   api.DanmuProtol(binary.BigEndian.Uint16(data[6:8])),
		OpCode:     api.DanmuOp(binary.BigEndian.Uint32(data[8:12])),
		Sequence:   binary.BigEndian.Uint32(data[12:16]),
	}
	if header.HeaderSize != 16 || header.Size < 16 || header.Size > maxPacketSize ||
		header.ProtoVer > api.DanmuProtolNormalBrotli || header.OpCode > api.DanmuOpAuthResp {
		return nil, invalidMessageErr
	}
	return header, nil
}

// A partial TCP frame is kept for the next read; invalid frames terminate the
// connection instead of panicking or retaining an unbounded buffer forever.
func unpackMessages(room *api.LiveRoom, data []byte, done <-chan struct{}, depth int) (consumed uint32, err error) {
	if depth > 4 {
		return 0, invalidMessageErr
	}
	for len(data) > 0 {
		header, err := parseHeader(data)
		if errors.Is(err, headerNotCompleteErr) {
			return consumed, nil
		}
		if err != nil {
			return consumed, err
		}
		if int(header.Size) > len(data) {
			return consumed, nil
		}
		body := data[header.HeaderSize:header.Size]
		data = data[header.Size:]
		consumed += header.Size
		if header.ProtoVer == api.DanmuProtolNormalZlib || header.ProtoVer == api.DanmuProtolNormalBrotli {
			var reader io.Reader
			if header.ProtoVer == api.DanmuProtolNormalZlib {
				zr, err := zlib.NewReader(bytes.NewReader(body))
				if err != nil {
					return consumed, fmt.Errorf("decompress zlib: %w", err)
				}
				defer zr.Close()
				reader = zr
			} else {
				reader = brotli.NewReader(bytes.NewReader(body))
			}
			decoded, err := io.ReadAll(io.LimitReader(reader, maxPacketSize+1))
			if err != nil || len(decoded) > maxPacketSize {
				return consumed, fmt.Errorf("invalid compressed message: %v", err)
			}
			n, err := unpackMessages(room, decoded, done, depth+1)
			if err != nil {
				return consumed, err
			}
			if int(n) != len(decoded) {
				return consumed, invalidMessageErr
			}
			continue
		}
		switch header.OpCode {
		case api.DanmuOpHeartBeatResp:
			if len(body) >= 4 {
				atomic.StoreUint32(&room.Hot, binary.BigEndian.Uint32(body))
			}
		case api.DanmuOpNormal:
			message := new(api.DanmuMessage)
			if err := json.Unmarshal(body, message); err != nil {
				logging.Errorf("unmarshal normal message error, err=%v", err)
				continue
			}
			select {
			case room.MessageChan <- message:
			case <-done:
				return consumed, context.Canceled
			case <-room.DoneChan:
				return consumed, context.Canceled
			}
		}
	}
	return consumed, nil
}

func unpackMessage(room *api.LiveRoom, data []byte) uint32 {
	n, err := unpackMessages(room, data, room.DoneChan, 0)
	if err != nil {
		logging.Errorf("unpack message error, err=%v", err)
	}
	return n
}

func GetDanmuInfo(client *http.Client, id uint64) (*api.DanmuInfoResp, error) {
	return getDanmuInfo(context.Background(), client, id)
}

func getDanmuInfo(ctx context.Context, client *http.Client, id uint64) (*api.DanmuInfoResp, error) {
	v, err := query.Values(api.DanmuInfoReq{ID: id})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	signedURL, err := signURLContext(ctx, client, "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo?"+v.Encode())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get danmu info HTTP %d", resp.StatusCode)
	}
	var info api.DanmuInfoResp
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	if info.Code != 0 {
		return nil, fmt.Errorf("get danmu info code %d: %s", info.Code, info.Message)
	}
	return &info, nil
}

func writePacket(conn net.Conn, packet []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	for len(packet) > 0 {
		n, err := conn.Write(packet)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	return nil
}

func authenticateDanmu(conn net.Conn, uid, roomID uint64, token string) error {
	if err := conn.SetDeadline(time.Now().Add(authTimeout)); err != nil {
		return err
	}
	defer conn.SetDeadline(time.Time{})
	body, err := json.Marshal(api.DanmuAuthPacketReq{
		UID: uid, RoomID: roomID, ProtoVer: 3, Platform: "web", Type: 2, Key: token,
	})
	if err != nil {
		return err
	}
	if err := writePacket(conn, packMessage(body, api.DanmuProtolHeartBeat, api.DanmuOpAuth, 1)); err != nil {
		return err
	}
	// TCP may split the auth response, or coalesce it with the first danmu.
	// Read exactly one frame, leaving subsequent messages in the connection.
	headerBytes := make([]byte, 16)
	if _, err := io.ReadFull(conn, headerBytes); err != nil {
		return err
	}
	header, err := parseHeader(headerBytes)
	if err != nil {
		return err
	}
	if header.OpCode != api.DanmuOpAuthResp {
		return errors.New("unexpected auth response operation")
	}
	body = make([]byte, int(header.Size)-16)
	if _, err := io.ReadFull(conn, body); err != nil {
		return err
	}
	var response struct {
		Code *int `json:"code"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return err
	}
	if response.Code == nil || *response.Code != 0 {
		return fmt.Errorf("connect server auth failed: %s", body)
	}
	return nil
}

func connectDanmuServer(uid, roomID uint64, info *api.DanmuInfoResp) (net.Conn, error) {
	return connectDanmuServerContext(context.Background(), uid, roomID, info)
}

func connectDanmuServerContext(ctx context.Context, uid, roomID uint64, info *api.DanmuInfoResp) (net.Conn, error) {
	var lastErr error
	dialer := net.Dialer{Timeout: time.Second}
	for _, host := range info.Data.HostList {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host.Host, strconv.Itoa(host.Port)))
		if err == nil {
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			err = authenticateDanmu(conn, uid, roomID, info.Data.Token)
			stop()
			if err == nil && ctx.Err() == nil {
				return conn, nil
			}
			_ = conn.Close()
		}
		lastErr = err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no danmu server can connect: %v", lastErr)
}

func newLiveRoom(uid, roomID uint64, info *api.DanmuInfoResp, client *http.Client) (*api.LiveRoom, error) {
	return newLiveRoomContext(context.Background(), uid, roomID, info, client)
}

func newLiveRoomContext(ctx context.Context, uid, roomID uint64, info *api.DanmuInfoResp, client *http.Client) (*api.LiveRoom, error) {
	conn, err := connectDanmuServerContext(ctx, uid, roomID, info)
	if err != nil {
		return nil, err
	}
	return &api.LiveRoom{
		UID: uid, RoomID: roomID, Seq: 1, Client: client,
		MessageChan: make(chan *api.DanmuMessage, 128),
		ReqChan:     make(chan []byte, 10), DoneChan: make(chan struct{}), StreamConn: conn,
	}, nil
}

func ConnectDanmuServer(uid, roomID uint64, info *api.DanmuInfoResp) (*api.LiveRoom, error) {
	room, err := newLiveRoom(uid, roomID, info, http.DefaultClient)
	if err == nil {
		go monitorConn(room)
	}
	return room, err
}

func heartBeatPacket(room *api.LiveRoom) []byte {
	seq := atomic.AddUint32(&room.Seq, 1)
	return packMessage([]byte("[object Object]"), api.DanmuProtolHeartBeat, api.DanmuOpHeartBeat, seq)
}

func processWrite(room *api.LiveRoom, conn net.Conn, done <-chan struct{}) error {
	if err := writePacket(conn, heartBeatPacket(room)); err != nil {
		return err
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		var packet []byte
		select {
		case <-done:
			return nil
		case <-ticker.C:
			packet = heartBeatPacket(room)
		case packet = <-room.ReqChan:
		}
		if err := writePacket(conn, packet); err != nil {
			return err
		}
	}
}

func processRead(room *api.LiveRoom, conn net.Conn, done <-chan struct{}) error {
	var pending []byte
	buffer := make([]byte, 64*1024)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return err
		}
		n, readErr := conn.Read(buffer)
		if n > 0 {
			pending = append(pending, buffer[:n]...)
			consumed, err := unpackMessages(room, pending, done, 0)
			if err != nil {
				return err
			}
			pending = pending[consumed:]
			if len(pending) == 0 {
				pending = nil
			}
		}
		if readErr != nil {
			return readErr
		}
		select {
		case <-done:
			return nil
		default:
		}
	}
}

// One supervisor owns the current connection. Workers capture their connection
// and per-connection cancellation channel; the room's DoneChan never changes.
func runConnection(room *api.LiveRoom, conn net.Conn) {
	done := make(chan struct{})
	failed := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); failed <- processRead(room, conn, done) }()
	go func() { defer workers.Done(); failed <- processWrite(room, conn, done) }()
	select {
	case <-room.DoneChan:
	case err := <-failed:
		logging.Warnf("danmu connection interrupted, err=%v", err)
	}
	close(done)
	_ = conn.Close() // Unblocks both Read and Write before starting new workers.
	workers.Wait()
}

func reconnectLoop(ctx context.Context, dial func() (net.Conn, error), delay time.Duration) net.Conn {
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		conn, err := dial()
		if err == nil {
			if ctx.Err() != nil {
				_ = conn.Close()
				return nil
			}
			return conn
		}
		logging.Warnf("retry connect danmu server failed, err=%v", err)
		if delay < 30*time.Second {
			delay = min(delay*2, 30*time.Second)
		}
	}
}

func monitorConn(room *api.LiveRoom) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-room.DoneChan:
			cancel()
		case <-ctx.Done():
		}
	}()
	conn := room.StreamConn
	for conn != nil {
		runConnection(room, conn)
		select {
		case <-room.DoneChan:
			return
		default:
		}
		conn = reconnectLoop(ctx, func() (net.Conn, error) {
			info, err := getDanmuInfo(ctx, room.Client, room.RoomID)
			if err != nil {
				return nil, err
			}
			return connectDanmuServerContext(ctx, room.UID, room.RoomID, info)
		}, time.Second)
		if conn != nil {
			logging.Infof("retry connect danmu server success")
		}
	}
}
