package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// forwardPostCounter 让一次转发请求里同一条源帖子只计一次转发：计数跟着第一条真正写入的转发走，
// 重放不算写入。
func TestForwardPostCounter(t *testing.T) {
	post := &domain.ChannelPostRef{ChannelID: 10, MessageID: 5}
	samePost := &domain.ChannelPostRef{ChannelID: 10, MessageID: 5}
	otherPost := &domain.ChannelPostRef{ChannelID: 10, MessageID: 6}

	t.Run("a source that is not a post is never counted", func(t *testing.T) {
		c := forwardPostCounter{}
		if got := c.pending(nil); got != nil {
			t.Fatalf("pending(nil) = %+v, want nil", got)
		}
		c.written(nil, false)
		if len(c) != 0 {
			t.Fatalf("counter = %v after writing a non-post, want empty", c)
		}
	})

	t.Run("the same post is counted once per request", func(t *testing.T) {
		c := forwardPostCounter{}
		if got := c.pending(post); got == nil || *got != *post {
			t.Fatalf("first pending = %+v, want %+v", got, post)
		}
		c.written(post, false)
		if got := c.pending(samePost); got != nil {
			t.Fatalf("pending after the post was counted = %+v, want nil", got)
		}
	})

	t.Run("different posts are counted separately", func(t *testing.T) {
		c := forwardPostCounter{}
		c.written(post, false)
		if got := c.pending(otherPost); got == nil || *got != *otherPost {
			t.Fatalf("pending(other post) = %+v, want %+v", got, otherPost)
		}
	})

	t.Run("a replay does not use up the count", func(t *testing.T) {
		c := forwardPostCounter{}
		c.written(post, true)
		if got := c.pending(samePost); got == nil || *got != *post {
			t.Fatalf("pending after a replay = %+v, want %+v (the next copy still counts)", got, post)
		}
		c.written(samePost, false)
		if got := c.pending(post); got != nil {
			t.Fatalf("pending after the real write = %+v, want nil", got)
		}
	})
}
