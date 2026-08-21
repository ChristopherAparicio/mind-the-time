package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidCommuteName = errors.New("invalid commute name")

const maximumCommuteNameTextLength = 80

type CommuteName struct {
	text string
}

func NewCommuteName(text string) (CommuteName, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > maximumCommuteNameTextLength {
		return CommuteName{}, ErrInvalidCommuteName
	}
	return CommuteName{text: text}, nil
}

func (n CommuteName) Text() string {
	return n.text
}
