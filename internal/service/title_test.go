package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"openpak/chat/internal/store"
)

func TestRateLimiterWindow(t *testing.T) {
	var r rateLimiter
	now := time.Date(2026, 11, 5, 12, 0, 10, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if !r.allow("a", 3, now) {
			t.Fatalf("send %d refused", i)
		}
	}
	if r.allow("a", 3, now) {
		t.Fatal("fourth send in the window allowed")
	}
	if !r.allow("b", 3, now) {
		t.Fatal("another sender limited")
	}
	if !r.allow("a", 3, now.Add(time.Minute)) {
		t.Fatal("next window still limited")
	}
}

// Argument errors come back before the core or the store is touched (both nil here).
func TestSendTitleValidation(t *testing.T) {
	s := &Service{}
	a, c := "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333"
	cases := []struct {
		m    store.TitleMessage
		want error
	}{
		{store.TitleMessage{Network: "3ds", FromAccount: a, ToAccount: c}, ErrTitleArgs},
		{store.TitleMessage{Title: "t", FromAccount: a, ToAccount: c}, ErrTitleArgs},
		{store.TitleMessage{Title: "t", Network: "3ds", FromAccount: a}, ErrTitleArgs},
		{store.TitleMessage{Title: "t", Network: "3ds", FromAccount: a, ToAccount: c, ToRoom: "r"}, ErrTitleArgs},
		{store.TitleMessage{Title: "t", Network: "3ds", FromAccount: a, ToAccount: "x"}, ErrBadAccount},
		{store.TitleMessage{Title: "t", Network: "3ds", FromAccount: a, ToAccount: a}, ErrSameTalker},
		{store.TitleMessage{Title: "t", Network: "3ds", FromAccount: a, ToRoom: "r", BodyKind: "png"}, ErrBadKind},
		{store.TitleMessage{Title: "t", Network: "3ds", FromAccount: a, ToRoom: "r", Body: make([]byte, MaxTitleBodyBytes+1)}, ErrTooLarge},
		{store.TitleMessage{Title: "t", Network: "3ds", FromAccount: a, ToRoom: "r", ParentID: -1}, ErrTitleArgs},
	}
	for i, tc := range cases {
		if _, _, err := s.SendTitle(context.Background(), tc.m, 0, ""); !errors.Is(err, tc.want) {
			t.Errorf("case %d: %v, want %v", i, err, tc.want)
		}
	}
	if _, err := s.DeleteTitle(context.Background(), store.Inbox{Title: "t", Network: "3ds", Account: a}, nil, false); !errors.Is(err, ErrTitleArgs) {
		t.Errorf("delete with neither ids nor all: %v", err)
	}
}
