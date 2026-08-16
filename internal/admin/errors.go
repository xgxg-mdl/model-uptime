// Package admin 实现管理配置的业务命令与一致性事务。
package admin

import (
	"errors"
	"fmt"
)

// ErrorKind 描述调用方可以稳定映射的失败类别。
type ErrorKind uint8

const (
	ErrorInvalid ErrorKind = iota + 1
	ErrorNotFound
	ErrorConflict
	ErrorInternal
)

// Error 保留面向用户的消息，同时隐藏底层持久化与运行时细节。
type Error struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "admin operation failed"
}

func (e *Error) Unwrap() error { return e.Cause }

func KindOf(err error) ErrorKind {
	var target *Error
	if errors.As(err, &target) {
		return target.Kind
	}
	return ErrorInternal
}

func invalid(message string) error {
	return &Error{Kind: ErrorInvalid, Message: message}
}

func notFound(format string, args ...any) error {
	return &Error{Kind: ErrorNotFound, Message: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &Error{Kind: ErrorConflict, Message: fmt.Sprintf(format, args...)}
}

func internal(message string, cause error) error {
	return &Error{Kind: ErrorInternal, Message: message, Cause: cause}
}
