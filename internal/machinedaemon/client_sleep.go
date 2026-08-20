package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

var (
	errDaemonIdleSleep           = errors.New("daemon idle sleep requested")
	errDaemonAsleep              = errors.New("daemon machine asleep")
	errDaemonSleepVetoed         = errors.New("daemon sleep vetoed")
	errDaemonSleepNotWakeCapable = errors.New("daemon machine is not wake capable")
)

const (
	daemonIdleTickInterval = time.Second
	wakeClockJumpThreshold = 5 * time.Second
)

func (c *Client) sleepEnabled() bool {
	return c.cfg.SleepAfter > 0 && !c.sleepDisabled.Load()
}

func (c *Client) daemonIdle(ctx context.Context) bool {
	c.processMu.RLock()
	busy := false
	for _, runtime := range c.processes {
		if runtime != nil && !runtime.cleanupOnly {
			busy = true
			break
		}
	}
	c.processMu.RUnlock()
	if busy {
		return false
	}
	reports, err := c.outboxReports(ctx, nil)
	return err == nil && len(reports) == 0
}

func (c *Client) Sleep(ctx context.Context, runtime DaemonRuntime) error {
	err := c.postJSON(ctx, daemonAPIPath+"/runtimes/"+runtime.ID+"/sleep", nil, nil)
	var statusErr httpStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusConflict:
			if sleepConflictCode(statusErr.Body) == "not_wake_capable" {
				return errDaemonSleepNotWakeCapable
			}
			return errDaemonSleepVetoed
		case http.StatusGone:
			return errDaemonRuntimeUnregistered
		}
	}
	return err
}

func sleepConflictCode(body string) string {
	var conflict struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &conflict); err != nil {
		return ""
	}
	return conflict.Code
}

func (c *Client) startWakeListener(ctx context.Context) (string, error) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", c.cfg.WakeListenAddr)
	if err != nil {
		return "", fmt.Errorf("bind daemon wake listener %s: %w", c.cfg.WakeListenAddr, err)
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				c.log.Warn("daemon wake listener accept failed; retrying", "error", err)
				if sleepContext(ctx, time.Second) != nil {
					return
				}
				continue
			}
			c.signalWake()
			go answerWakePoke(conn)
		}
	}()
	address := listener.Addr().String()
	c.log.Info("daemon wake listener bound", "address", address)
	return address, nil
}

func answerWakePoke(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _ = io.CopyN(io.Discard, conn, 4096)
}

func (c *Client) signalWake() {
	select {
	case c.wakeSignals <- struct{}{}:
	default:
	}
}

func (c *Client) drainWakeSignals() {
	select {
	case <-c.wakeSignals:
	default:
	}
}

func (c *Client) sleepUntilWake(ctx context.Context) error {
	c.http.CloseIdleConnections()
	if err := c.sleepPlatform.allowSleep(); err != nil {
		return err
	}
	c.log.Info("daemon asleep; waiting for wake signal")
	ticker := time.NewTicker(daemonIdleTickInterval)
	defer ticker.Stop()
	lastWall := time.Now().Round(0)
	for {
		select {
		case <-ctx.Done():
			_ = c.sleepPlatform.preventSleep()
			return ctx.Err()
		case <-c.wakeSignals:
			c.log.Info("daemon waking", "source", "wake_listener")
			return c.sleepPlatform.preventSleep()
		case <-ticker.C:
			nowWall := time.Now().Round(0)
			if gap := nowWall.Sub(lastWall); gap >= wakeClockJumpThreshold {
				c.log.Info("daemon waking", "source", "clock_jump", "gap", gap)
				return c.sleepPlatform.preventSleep()
			}
			lastWall = nowWall
		}
	}
}
