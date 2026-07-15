package mail

import (
	"errors"
	"fmt"
)

var (
	// ErrMessageTooLarge indicates that the raw message exceeded MaxRawBytes.
	ErrMessageTooLarge = errors.New("raw message exceeds parser limit")
	// ErrHeaderTooLarge indicates that one header block or all header blocks exceeded their byte limit.
	ErrHeaderTooLarge = errors.New("MIME headers exceed parser limit")
	// ErrTooManyHeaders indicates that the message contained too many header fields.
	ErrTooManyHeaders = errors.New("MIME header count exceeds parser limit")
	// ErrTooManyParts indicates that the message contained too many MIME entities.
	ErrTooManyParts = errors.New("MIME part count exceeds parser limit")
	// ErrNestingTooDeep indicates that the MIME entity tree exceeded MaxDepth.
	ErrNestingTooDeep = errors.New("MIME nesting exceeds parser limit")
	// ErrPartTooLarge indicates that one decoded MIME leaf exceeded MaxPartBytes.
	ErrPartTooLarge = errors.New("decoded MIME part exceeds parser limit")
	// ErrDecodedContentTooLarge indicates that all decoded leaves exceeded MaxDecodedBytes.
	ErrDecodedContentTooLarge = errors.New("decoded MIME content exceeds parser limit")
)

// Limits defines parser resource ceilings. Zero values are invalid so that a
// missing limit can never silently become unbounded.
type Limits struct {
	MaxRawBytes         int64
	MaxHeaderBytes      int64
	MaxTotalHeaderBytes int64
	MaxHeaders          int
	MaxParts            int
	MaxDepth            int
	MaxDecodedBytes     int64
	MaxPartBytes        int64
	MaxTextBytes        int
	MaxHTMLBytes        int
	MaxSubjectBytes     int
	MaxFilenameBytes    int
	MaxAddresses        int
	MaxWarnings         int
}

// DefaultLimits returns conservative limits for the Reference Profile.
func DefaultLimits() Limits {
	return Limits{
		MaxRawBytes:         25 << 20,
		MaxHeaderBytes:      64 << 10,
		MaxTotalHeaderBytes: 1 << 20,
		MaxHeaders:          1_000,
		MaxParts:            100,
		MaxDepth:            10,
		MaxDecodedBytes:     32 << 20,
		MaxPartBytes:        25 << 20,
		MaxTextBytes:        512 << 10,
		MaxHTMLBytes:        1 << 20,
		MaxSubjectBytes:     998,
		MaxFilenameBytes:    255,
		MaxAddresses:        100,
		MaxWarnings:         100,
	}
}

func (l Limits) validate() error {
	positiveInt64 := []struct {
		name  string
		value int64
	}{
		{name: "MaxRawBytes", value: l.MaxRawBytes},
		{name: "MaxHeaderBytes", value: l.MaxHeaderBytes},
		{name: "MaxTotalHeaderBytes", value: l.MaxTotalHeaderBytes},
		{name: "MaxDecodedBytes", value: l.MaxDecodedBytes},
		{name: "MaxPartBytes", value: l.MaxPartBytes},
	}
	for _, limit := range positiveInt64 {
		if limit.value <= 0 {
			return fmt.Errorf("mail parser %s must be positive", limit.name)
		}
	}
	positiveInt := []struct {
		name  string
		value int
	}{
		{name: "MaxHeaders", value: l.MaxHeaders},
		{name: "MaxParts", value: l.MaxParts},
		{name: "MaxDepth", value: l.MaxDepth},
		{name: "MaxTextBytes", value: l.MaxTextBytes},
		{name: "MaxHTMLBytes", value: l.MaxHTMLBytes},
		{name: "MaxSubjectBytes", value: l.MaxSubjectBytes},
		{name: "MaxFilenameBytes", value: l.MaxFilenameBytes},
		{name: "MaxAddresses", value: l.MaxAddresses},
		{name: "MaxWarnings", value: l.MaxWarnings},
	}
	for _, limit := range positiveInt {
		if limit.value <= 0 {
			return fmt.Errorf("mail parser %s must be positive", limit.name)
		}
	}
	if l.MaxHeaderBytes > l.MaxTotalHeaderBytes {
		return errors.New("mail parser MaxHeaderBytes must not exceed MaxTotalHeaderBytes")
	}
	if l.MaxPartBytes > l.MaxDecodedBytes {
		return errors.New("mail parser MaxPartBytes must not exceed MaxDecodedBytes")
	}
	return nil
}
