package redistore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const connectTimeout = 5 * time.Second

type Client struct {
	client *redis.Client
}

func Connect(rawURL string) (*Client, error) {
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}
	return &Client{client: client}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("redis client is closed")
	}
	return c.client.Ping(ctx).Err()
}

func (c *Client) EvalInt(ctx context.Context, script string, keys []string, args ...any) (int, error) {
	if c == nil || c.client == nil {
		return 0, fmt.Errorf("redis client is closed")
	}
	return c.client.Eval(ctx, script, keys, args...).Int()
}

func (c *Client) EvalBytes(ctx context.Context, script string, keys []string, args ...any) ([]byte, bool, error) {
	if c == nil || c.client == nil {
		return nil, false, fmt.Errorf("redis client is closed")
	}
	value, err := c.client.Eval(ctx, script, keys, args...).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	switch raw := value.(type) {
	case string:
		return []byte(raw), true, nil
	case []byte:
		return raw, true, nil
	case nil:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("redis eval returned %T", value)
	}
}

func (c *Client) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("redis client is closed")
	}
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c *Client) SetNX(ctx context.Context, key string, value any, ttl time.Duration) (bool, error) {
	if c == nil || c.client == nil {
		return false, fmt.Errorf("redis client is closed")
	}
	return c.client.SetNX(ctx, key, value, ttl).Result()
}

func (c *Client) GetBytes(ctx context.Context, key string) ([]byte, bool, error) {
	if c == nil || c.client == nil {
		return nil, false, fmt.Errorf("redis client is closed")
	}
	raw, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func (c *Client) GetDelBytes(ctx context.Context, key string) ([]byte, bool, error) {
	if c == nil || c.client == nil {
		return nil, false, fmt.Errorf("redis client is closed")
	}
	raw, err := c.client.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func (c *Client) Publish(ctx context.Context, channel string, payload []byte) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("redis client is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.client.Publish(ctx, channel, payload).Err()
}

type Subscription struct {
	cancel context.CancelFunc
	pubsub *redis.PubSub
	done   chan struct{}
	once   sync.Once
	err    error
}

// Subscribe invokes handler with a fresh payload slice; the handler may retain it.
func (c *Client) Subscribe(
	ctx context.Context,
	channel string,
	log *slog.Logger,
	handler func(context.Context, string, []byte),
) (*Subscription, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("redis client is closed")
	}
	pubsub := c.client.Subscribe(ctx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}
	subCtx, cancel := context.WithCancel(context.Background())
	sub := &Subscription{cancel: cancel, pubsub: pubsub, done: make(chan struct{})}
	messages := pubsub.Channel(redis.WithChannelSize(1024))
	go func() {
		defer close(sub.done)
		defer func() { _ = pubsub.Close() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-subCtx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					if log != nil {
						log.Warn("redis pubsub channel closed", "channel", channel)
					}
					return
				}
				invokeSubscriptionHandler(log, handler, ctx, msg.Channel, []byte(msg.Payload))
			}
		}
	}()
	return sub, nil
}

func invokeSubscriptionHandler(
	log *slog.Logger,
	handler func(context.Context, string, []byte),
	ctx context.Context,
	channel string,
	payload []byte,
) {
	defer func() {
		if recovered := recover(); recovered != nil && log != nil {
			log.Error(
				"redis subscription handler panicked",
				"channel", channel,
				"error", fmt.Sprint(recovered),
			)
		}
	}()
	handler(ctx, channel, payload)
}

func (s *Subscription) Unsubscribe() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.cancel()
		err := s.pubsub.Close()
		if errors.Is(err, redis.ErrClosed) {
			err = nil
		}
		<-s.done
		s.err = err
	})
	return s.err
}

func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}
