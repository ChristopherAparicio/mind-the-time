package domain

import (
	"errors"
	"time"
)

var ErrInvalidTransferBuffer = errors.New("invalid transfer buffer")

type TransferBuffer struct {
	duration time.Duration
}

func NewTransferBuffer(duration time.Duration) (TransferBuffer, error) {
	if duration < 0 {
		return TransferBuffer{}, ErrInvalidTransferBuffer
	}
	return TransferBuffer{duration: duration}, nil
}

func (b TransferBuffer) Duration() time.Duration {
	return b.duration
}

func (b TransferBuffer) valid() bool {
	return b.duration >= 0
}
