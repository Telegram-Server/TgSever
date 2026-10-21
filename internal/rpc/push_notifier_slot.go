package rpc

import "sync/atomic"

// notifierSlot 是 router 上一个离线推送入口的原子槽位。推送服务依赖 router 的 presence，
// 只能在 router 创建之后经各自的 Set…Notifier 注入，而处理 RPC 与分发器的 goroutine 并发读取它。
// 存 nil 表示关闭。
type notifierSlot[T any] struct {
	p atomic.Pointer[T]
}

func (s *notifierSlot[T]) set(n T) {
	if any(n) == nil {
		s.p.Store(nil)
		return
	}
	s.p.Store(&n)
}

// load 返回注入的入口；未注入（或已关闭）时 ok=false。
func (s *notifierSlot[T]) load() (n T, ok bool) {
	if p := s.p.Load(); p != nil {
		return *p, true
	}
	return n, false
}
