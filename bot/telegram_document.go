package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
)

type TelegramDocument struct {
	Name      string
	Data      []byte
	AfterSend func() string
}

func (b *Bot) sendOrchestratorDocument(ctx context.Context, document *TelegramDocument) string {
	if err := b.sendDocument(ctx, document); err != nil {
		return "Telegram не подтвердил отправку файла. Аккаунты и игра сохранены."
	}
	if document.AfterSend != nil {
		if ctx.Err() != nil {
			return "Файл отправлен. Операция удаления отменена."
		}
		return "Файл отправлен. " + document.AfterSend()
	}
	return "Файл отправлен. Храните его приватно: он содержит учётные данные Twitch."
}

func (b *Bot) sendDocument(ctx context.Context, document *TelegramDocument) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(b.adminID, 10)); err != nil {
		return err
	}
	if err := form.WriteField("caption", "Файл содержит учётные данные Twitch. Храните его приватно."); err != nil {
		return err
	}
	file, err := form.CreateFormFile("document", document.Name)
	if err != nil {
		return err
	}
	if _, err = file.Write(document.Data); err != nil {
		return err
	}
	if err = form.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.token+"/sendDocument", &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	client := *b.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result struct{ OK bool }
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&result) != nil || !result.OK {
		return fmt.Errorf("telegram: document upload failed")
	}
	return nil
}
