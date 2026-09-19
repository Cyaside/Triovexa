package storage

import "context"

type SettingsStore interface {
	GetSetting(context.Context, string) (string, error)
	PutSetting(context.Context, string, string) error
}
