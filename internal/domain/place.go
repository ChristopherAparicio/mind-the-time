package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidPlace = errors.New("invalid place")

const maximumPlaceTextLength = 256

type Place struct {
	text string
}

func NewPlace(text string) (Place, error) {
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > maximumPlaceTextLength {
		return Place{}, ErrInvalidPlace
	}
	return Place{text: text}, nil
}

func (p Place) Text() string {
	return p.text
}

func (p Place) valid() bool {
	return p.text != "" && utf8.ValidString(p.text) && utf8.RuneCountInString(p.text) <= maximumPlaceTextLength
}
