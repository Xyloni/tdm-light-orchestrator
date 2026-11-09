package parser

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var ErrNoAccounts = errors.New("no accounts in account file")

// Account — один аккаунт Twitch из account.txt.
type Account struct {
	Email    string
	Login    string
	Password string
	OAuth    string // oauth_token (без префикса oauth:)
	UserID   string // user_ID
	DeviceID string // device_ID
	Group    int    // номер группы по маркеру "N:"
	Line     int    // номер строки в файле (для диагностики и разброса стартов)
}

var groupRe = regexp.MustCompile(`^\s*(\d+)\s*:\s*$`)

// ParseFile читает account.txt с групповой структурой:
//
//	1:
//	email:login:password:oauth_token:user_ID:device_ID
//	2:
//	email:login:password:oauth_token:user_ID:device_ID
//
// Возвращает группы, отсортированные по возрастанию номера.
// Пустые строки и строки, начинающиеся с '#', игнорируются.
// Если маркеров нет, все аккаунты попадают в группу 1.
func ParseFile(path string) ([][]*Account, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	groups, err := Parse(f)
	if errors.Is(err, ErrNoAccounts) {
		return nil, fmt.Errorf("%w: %s", ErrNoAccounts, path)
	}
	return groups, err
}

// Parse reads the same account format from a stream.
func Parse(reader io.Reader) ([][]*Account, error) {
	var err error
	groups := map[int][]*Account{}
	seenLogins := make(map[string]int)
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	curGroup := 1
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := groupRe.FindStringSubmatch(line); m != nil {
			curGroup, err = strconv.Atoi(m[1])
			if err != nil || curGroup < 1 {
				return nil, fmt.Errorf("строка %d: неверный номер группы", lineNo)
			}
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 6 {
			return nil, fmt.Errorf("строка %d: ожидалось 6 полей через ':' (email:login:password:oauth_token:user_ID:device_ID), получено %d", lineNo, len(parts))
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		a := &Account{
			Email:    parts[0],
			Login:    parts[1],
			Password: parts[2],
			OAuth:    strings.TrimPrefix(parts[3], "oauth:"),
			UserID:   parts[4],
			DeviceID: parts[5],
			Group:    curGroup,
			Line:     lineNo,
		}
		if a.OAuth == "" || a.UserID == "" {
			return nil, fmt.Errorf("строка %d: oauth_token и user_ID обязательны", lineNo)
		}
		if a.Login == "" {
			return nil, fmt.Errorf("строка %d: login обязателен", lineNo)
		}
		for _, ch := range a.Login {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
				return nil, fmt.Errorf("строка %d: login может содержать только латинские буквы, цифры и '_'", lineNo)
			}
		}
		a.Login = strings.ToLower(a.Login)
		if previous, exists := seenLogins[a.Login]; exists {
			return nil, fmt.Errorf("строка %d: повтор login из строки %d", lineNo, previous)
		}
		seenLogins[a.Login] = lineNo
		groups[curGroup] = append(groups[curGroup], a)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, ErrNoAccounts
	}

	keys := make([]int, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := make([][]*Account, 0, len(keys))
	for _, k := range keys {
		out = append(out, groups[k])
	}
	return out, nil
}
