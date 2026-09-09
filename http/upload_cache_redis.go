package fbhttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisUploadCache is an upload cache for multi replica deployments
type redisUploadCache struct {
	client *redis.Client
}

func newRedisUploadCache(redisURL string) (*redisUploadCache, error) {
	if redisURL == "" {
		return nil, fmt.Errorf("redis URL is required")
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	// Test connection
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &redisUploadCache{client: client}, nil
}

func (c *redisUploadCache) filePathKey(filePath string) string {
	return "filebrowser:upload:" + filePath
}

// Register stores the upload length. The scoped removal callback is unused by
// the redis backend, which does not delete partial files on eviction.
func (c *redisUploadCache) Register(filePath string, fileSize int64, _ func() error) error {
	pipe := c.client.TxPipeline()
	key := c.filePathKey(filePath)
	pipe.Del(context.Background(), key)
	pipe.HSet(context.Background(), key, "length", fileSize, "stamp", "")
	pipe.Expire(context.Background(), key, uploadCacheTTL)
	_, err := pipe.Exec(context.Background())
	return err
}

func (c *redisUploadCache) Complete(filePath string) error {
	return c.client.Del(context.Background(), c.filePathKey(filePath)).Err()
}

func (c *redisUploadCache) Lock(filePath string) (func(), error) {
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	key := c.filePathKey(filePath) + ":lock"
	ok, err := c.client.SetNX(context.Background(), key, token, 10*time.Minute).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("upload is busy")
	}
	return func() {
		// Never remove a successor's lease if this process was paused.
		c.client.Eval(context.Background(), `if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) else return 0 end`, []string{key}, token)
	}, nil
}

func (c *redisUploadCache) GetLength(filePath string) (int64, error) {
	result, err := c.client.HGet(context.Background(), c.filePathKey(filePath), "length").Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, fmt.Errorf("no active upload found for the given path")
		}
		return 0, fmt.Errorf("redis error: %w", err)
	}

	size, err := strconv.ParseInt(result, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid upload length in cache: %w", err)
	}

	c.Touch(filePath)

	return size, nil
}

func (c *redisUploadCache) GetStamp(filePath string) (string, error) {
	return c.client.HGet(context.Background(), c.filePathKey(filePath), "stamp").Result()
}

func (c *redisUploadCache) SetStamp(filePath, stamp string) error {
	result, err := c.client.Eval(context.Background(), `if redis.call('exists', KEYS[1]) == 1 then redis.call('hset', KEYS[1], 'stamp', ARGV[1]); return 1 else return 0 end`, []string{c.filePathKey(filePath)}, stamp).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return fmt.Errorf("no active upload")
	}
	return nil
}

func (c *redisUploadCache) Touch(filePath string) {
	err := c.client.Expire(context.Background(), c.filePathKey(filePath), uploadCacheTTL).Err()
	if err != nil {
		log.Printf("failed to touch upload in redis cache: %v", err)
	}
}

func (c *redisUploadCache) Close() {
	c.client.Close()
}
