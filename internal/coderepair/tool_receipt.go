package coderepair

import (
	"context"
	"errors"
	"time"
)

var ErrToolReceiptMissing = errors.New("repair tool receipt not found")
var ErrToolReceiptMismatch = errors.New("repair tool receipt identity mismatch")

type ToolReceipt struct {
	AttemptID    string
	CallID       string
	Name         string
	ArgsSHA256   string
	Revision     string
	State        string
	ResultSealed string
	CreatedAt    time.Time
	CompletedAt  time.Time
}

type ToolReceiptClaim struct {
	Job             Job
	ExpectedVersion int64
}

// Every receipt operation is fenced against the current job and case version.
// A started receipt with no result is never silently retried after a crash.
type ToolReceiptStore interface {
	StartRepairTool(context.Context, ToolReceiptClaim, ToolReceipt) (ToolReceipt, error)
	CompleteRepairTool(context.Context, ToolReceiptClaim, ToolReceipt) error
	ListRepairToolReceipts(context.Context, ToolReceiptClaim) ([]ToolReceipt, error)
}
