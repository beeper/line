package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"golang.org/x/image/bmp"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/beeper/line/pkg/line"
)

type groupMetaRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn groupMetaRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestValidateLineGroupName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"Capture Test", nil},
		{"テスト 🎉 Ünïcode", nil},
		{strings.Repeat("1", 50), nil},
		{strings.Repeat("あ", 50), nil},
		{strings.Repeat("1", 51), errGroupNameTooLong},
		{strings.Repeat("1", 60), errGroupNameTooLong},
		// Emoji outside the BMP count twice, like JS string length.
		{strings.Repeat("🎉", 25), nil},
		{strings.Repeat("🎉", 26), errGroupNameTooLong},
		{"", errGroupNameEmpty},
		{"   ", errGroupNameEmpty},
	} {
		if err := validateLineGroupName(tc.name); !errors.Is(err, tc.want) {
			t.Errorf("validateLineGroupName(%q) = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestIsEditableGroupMID(t *testing.T) {
	for mid, want := range map[string]bool{
		"c0123": true,
		"C0123": true,
		"r0123": false,
		"u0123": false,
		"":      false,
	} {
		if got := isEditableGroupMID(mid); got != want {
			t.Errorf("isEditableGroupMID(%q) = %v, want %v", mid, got, want)
		}
	}
}

func TestCapabilitiesAdvertiseGroupNameAndAvatar(t *testing.T) {
	lc := &LineClient{}
	for _, tc := range []struct {
		id       networkid.PortalID
		roomType database.RoomType
		want     bool
	}{
		{"c0123", database.RoomTypeGroupDM, true},
		{"r0123", database.RoomTypeGroupDM, false},
		{"u0123", database.RoomTypeDM, false},
	} {
		portal := &bridgev2.Portal{Portal: &database.Portal{
			PortalKey: networkid.PortalKey{ID: tc.id},
			RoomType:  tc.roomType,
		}}
		caps := lc.GetCapabilities(context.Background(), portal)
		for _, evtType := range []event.Type{event.StateRoomName, event.StateRoomAvatar} {
			feature := caps.State[evtType.Type]
			got := feature != nil && feature.Level == event.CapLevelFullySupported
			if got != tc.want {
				t.Errorf("%s: %s supported = %v, want %v", tc.id, evtType.Type, got, tc.want)
			}
		}
	}
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not an image: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("output format = %s, want jpeg", format)
	}
	return img
}

func assertColorNear(t *testing.T, got color.Color, want color.RGBA) {
	t.Helper()
	r, g, b, _ := got.RGBA()
	near := func(a uint32, b uint8) bool {
		diff := int(a>>8) - int(b)
		return diff > -16 && diff < 16
	}
	if !near(r, want.R) || !near(g, want.G) || !near(b, want.B) {
		t.Fatalf("color = %d,%d,%d, want about %d,%d,%d", r>>8, g>>8, b>>8, want.R, want.G, want.B)
	}
}

func solidImage(c color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

var (
	red   = color.RGBA{R: 255, A: 255}
	blue  = color.RGBA{B: 255, A: 255}
	white = color.RGBA{R: 255, G: 255, B: 255, A: 255}
)

func TestConvertAvatarToJPEGKeepsJPEG(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, solidImage(red), nil); err != nil {
		t.Fatal(err)
	}
	out, err := convertAvatarToJPEG(context.Background(), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, buf.Bytes()) {
		t.Fatal("JPEG input was re-encoded")
	}
}

func TestConvertAvatarToJPEGFlattensTransparentPNG(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, solidImage(color.NRGBA{})); err != nil {
		t.Fatal(err)
	}
	out, err := convertAvatarToJPEG(context.Background(), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	assertColorNear(t, decodeJPEG(t, out).At(8, 8), white)
}

func TestConvertAvatarToJPEGUsesFirstGIFFrame(t *testing.T) {
	palette := color.Palette{red, blue}
	frame := func(idx uint8) *image.Paletted {
		img := image.NewPaletted(image.Rect(0, 0, 16, 16), palette)
		for i := range img.Pix {
			img.Pix[i] = idx
		}
		return img
	}
	var buf bytes.Buffer
	err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{frame(0), frame(1)}, Delay: []int{10, 10}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := convertAvatarToJPEG(context.Background(), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	assertColorNear(t, decodeJPEG(t, out).At(8, 8), red)
}

func TestConvertAvatarToJPEGConvertsBMP(t *testing.T) {
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, solidImage(blue)); err != nil {
		t.Fatal(err)
	}
	out, err := convertAvatarToJPEG(context.Background(), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	assertColorNear(t, decodeJPEG(t, out).At(8, 8), blue)
}

func TestConvertAvatarToJPEGFallsBackToFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	// No TIFF decoder is registered in Go, so this takes the same path as HEIC and AVIF.
	tiffData, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=blue:s=16x16", "-frames:v", "1",
		"-f", "image2pipe", "-vcodec", "tiff", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = image.Decode(bytes.NewReader(tiffData)); err == nil {
		t.Fatal("TIFF decoded in Go, so ffmpeg wasn't tested")
	}
	out, err := convertAvatarToJPEG(context.Background(), tiffData)
	if err != nil {
		t.Fatal(err)
	}
	assertColorNear(t, decodeJPEG(t, out).At(8, 8), blue)
}

func TestConvertAvatarToJPEGRejectsNonImage(t *testing.T) {
	if _, err := convertAvatarToJPEG(context.Background(), []byte("not an image")); err == nil {
		t.Fatal("expected an error")
	}
}

type recordedLineCall struct {
	path string
	body []byte
}

// useGroupMetaTestServer answers getChats with one group and records every call.
func useGroupMetaTestServer(t *testing.T) *[]recordedLineCall {
	t.Helper()
	oldNewClient := newLineAPIClient
	t.Cleanup(func() { newLineAPIClient = oldNewClient })
	var calls []recordedLineCall
	newLineAPIClient = func(token string) *line.Client {
		client := line.NewClient(token)
		client.HTTPClient = &http.Client{Transport: groupMetaRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			calls = append(calls, recordedLineCall{path: req.URL.Path, body: body})
			response := `{"code":0,"message":"ok","data":null}`
			if strings.HasSuffix(req.URL.Path, "/getChats") {
				response = `{"code":0,"data":{"chats":[{"type":0,"chatMid":"c1","chatName":"Old","picturePath":"/old"}]}}`
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(response)),
			}, nil
		})}
		return client
	}
	return &calls
}

func newGroupMetaTestClient() (*LineClient, *bridgev2.Portal) {
	lc := &LineClient{
		AccessToken: "access",
		UserLogin: &bridgev2.UserLogin{
			Bridge: &bridgev2.Bridge{Log: zerolog.New(io.Discard)},
		},
	}
	portal := &bridgev2.Portal{Portal: &database.Portal{
		PortalKey:  networkid.PortalKey{ID: "c1"},
		RoomType:   database.RoomTypeGroupDM,
		Name:       "Old",
		NameSet:    true,
		AvatarID:   "/old",
		AvatarMXC:  "mxc://example.com/old",
		AvatarHash: [32]byte{1},
		AvatarSet:  true,
	}}
	return lc, portal
}

func decodeUpdateChat(t *testing.T, body []byte) (map[string]any, float64) {
	t.Helper()
	var args []struct {
		Chat             map[string]any `json:"chat"`
		UpdatedAttribute float64        `json:"updatedAttribute"`
	}
	if err := json.Unmarshal(body, &args); err != nil || len(args) != 1 {
		t.Fatalf("updateChat body = %s (%v)", body, err)
	}
	return args[0].Chat, args[0].UpdatedAttribute
}

func TestHandleMatrixRoomNameSendsUpdateChat(t *testing.T) {
	calls := useGroupMetaTestServer(t)
	lc, portal := newGroupMetaTestClient()
	changed, err := lc.HandleMatrixRoomName(context.Background(), &bridgev2.MatrixRoomName{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
			Content: &event.RoomNameEventContent{Name: "テスト 🎉"},
			Portal:  portal,
		},
	})
	if err != nil || !changed {
		t.Fatalf("changed, err = %v, %v", changed, err)
	}
	if len(*calls) != 2 || !strings.HasSuffix((*calls)[1].path, "/updateChat") {
		t.Fatalf("calls = %v, want getChats then updateChat", *calls)
	}
	chat, attribute := decodeUpdateChat(t, (*calls)[1].body)
	if chat["chatName"] != "テスト 🎉" || chat["picturePath"] != "/old" || attribute != 1 {
		t.Fatalf("chat = %#v, attribute = %v", chat, attribute)
	}
	if portal.Name != "テスト 🎉" || !portal.NameSet {
		t.Fatalf("portal name = %q, set = %v", portal.Name, portal.NameSet)
	}
}

func TestHandleMatrixRoomNameRejectsWithoutCalling(t *testing.T) {
	calls := useGroupMetaTestServer(t)
	lc, portal := newGroupMetaTestClient()
	for _, name := range []string{"", strings.Repeat("x", 51)} {
		_, err := lc.HandleMatrixRoomName(context.Background(), &bridgev2.MatrixRoomName{
			MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomNameEventContent]{
				Content: &event.RoomNameEventContent{Name: name},
				Portal:  portal,
			},
		})
		if err == nil {
			t.Errorf("name %q: expected an error", name)
		}
	}
	if len(*calls) != 0 || portal.Name != "Old" {
		t.Fatalf("calls = %d, portal name = %q", len(*calls), portal.Name)
	}
}

func TestHandleMatrixRoomAvatarRemovesPicture(t *testing.T) {
	calls := useGroupMetaTestServer(t)
	lc, portal := newGroupMetaTestClient()
	changed, err := lc.HandleMatrixRoomAvatar(context.Background(), &bridgev2.MatrixRoomAvatar{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomAvatarEventContent]{
			Content: &event.RoomAvatarEventContent{},
			Portal:  portal,
		},
	})
	if err != nil || !changed {
		t.Fatalf("changed, err = %v, %v", changed, err)
	}
	if len(*calls) != 2 || !strings.HasSuffix((*calls)[1].path, "/updateChat") {
		t.Fatalf("calls = %v, want getChats then updateChat", *calls)
	}
	chat, attribute := decodeUpdateChat(t, (*calls)[1].body)
	if path, ok := chat["picturePath"]; !ok || path != "" || chat["chatName"] != "Old" || attribute != 2 {
		t.Fatalf("chat = %#v, attribute = %v", chat, attribute)
	}
	// An empty avatar ID matches what the op 121 resync reports for a removed picture.
	if portal.AvatarID != "" || portal.AvatarMXC != "" || portal.AvatarHash != [32]byte{} || !portal.AvatarSet {
		t.Fatalf("portal avatar = %q %q %x %v", portal.AvatarID, portal.AvatarMXC, portal.AvatarHash, portal.AvatarSet)
	}
}

func TestHandleMatrixRoomMetaRejectsLegacyRooms(t *testing.T) {
	calls := useGroupMetaTestServer(t)
	lc, portal := newGroupMetaTestClient()
	portal.ID = "r1"
	_, err := lc.HandleMatrixRoomAvatar(context.Background(), &bridgev2.MatrixRoomAvatar{
		MatrixEventBase: bridgev2.MatrixEventBase[*event.RoomAvatarEventContent]{
			Content: &event.RoomAvatarEventContent{},
			Portal:  portal,
		},
	})
	if !errors.Is(err, errGroupMetaUnsupported) || len(*calls) != 0 {
		t.Fatalf("err = %v, calls = %d", err, len(*calls))
	}
}
