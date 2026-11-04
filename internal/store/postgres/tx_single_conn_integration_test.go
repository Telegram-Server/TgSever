package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/pgtest"
)

// 持有事务时不能再去连接池借第二条连接：分配器回 PostgreSQL 查当前最大 id（测试分配器
// 每次都查，Redis 分配器在 key 缺失时查）若走连接池，并发数一旦达到连接池大小，每个
// 事务都占着一条连接等第二条，全部卡死。连接池默认按 CPU 数取大小，4 核的 CI 上
// 6 个并发转发就挂住了。这里把连接池压到 2 条，并发数远大于它，必须全部完成。
func TestTransactionsNeverBorrowASecondConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := Open(ctx, pgtest.TestDSN(t), WithMaxConns(2))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{AccessHash: 1501, Phone: "+179" + suffix + "01", FirstName: "SingleConnOwner"})
	if err != nil {
		t.Fatal(err)
	}

	const concurrency = 8
	run := func(name string, fn func(i int) error) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, concurrency)
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if err := fn(i); err != nil {
					errs <- fmt.Errorf("%s #%d: %w", name, i, err)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if ctx.Err() != nil {
			t.Fatalf("%s: timed out, a transaction waited for a second pooled connection", name)
		}
	}

	// 测试分配器：每次分配都回 PostgreSQL 查 MAX(id)。
	channels := newTestChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "single-conn " + suffix, Broadcast: true, Date: 1700002000})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	run("concurrent sends to one channel", func(i int) error {
		_, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: owner.ID, ChannelID: created.Channel.ID, RandomID: 7700 + int64(i), Message: "hello", Date: 1700002001,
		})
		return err
	})
	// 建频道同时分配频道 id 和创建服务消息的 id。
	run("concurrent channel creation", func(i int) error {
		_, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: fmt.Sprintf("single-conn %s %d", suffix, i), Megagroup: true, Date: 1700002002})
		return err
	})

	// 带转发头的私聊不走热路径，box id 在事务里分配。私聊按双方用户串行化，所以用
	// 互不相交的发送者/接收者对，让事务真的并发。
	users := make([]domain.User, 2*concurrency)
	for i := range users {
		if users[i], err = NewUserStore(pool).Create(ctx, domain.User{AccessHash: 1510 + int64(i), Phone: fmt.Sprintf("+179%s1%02d", suffix, i), FirstName: "SingleConnPeer"}); err != nil {
			t.Fatal(err)
		}
	}
	messages := newTestMessageStore(pool)
	run("concurrent private forwards", func(i int) error {
		_, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID: users[2*i].ID, RecipientUserID: users[2*i+1].ID, RandomID: 7800 + int64(i), Message: "fwd", Date: 1700002003,
			Forward: &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}, Date: 1700002001, ChannelPost: 2},
		})
		return err
	})
}
