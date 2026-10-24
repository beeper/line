package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
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
	out, err := convertAvatarToJPEG(buf.Bytes())
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
	out, err := convertAvatarToJPEG(buf.Bytes())
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
	out, err := convertAvatarToJPEG(buf.Bytes())
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
	out, err := convertAvatarToJPEG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	assertColorNear(t, decodeJPEG(t, out).At(8, 8), blue)
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestConvertAvatarToJPEGScalesDownLargeImages(t *testing.T) {
	var wide bytes.Buffer
	if err := jpeg.Encode(&wide, image.NewRGBA(image.Rect(0, 0, 5000, 100)), nil); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"png":  encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 100, 5000))),
		"jpeg": wide.Bytes(),
	} {
		out, err := convertAvatarToJPEG(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		bounds := decodeJPEG(t, out).Bounds()
		if max(bounds.Dx(), bounds.Dy()) != maxAvatarSide || min(bounds.Dx(), bounds.Dy()) != 81 {
			t.Errorf("%s: size = %dx%d, want 4096x81", name, bounds.Dx(), bounds.Dy())
		}
	}
}

func TestConvertAvatarToJPEGRejectsHugeImagesBeforeDecoding(t *testing.T) {
	// A real 1x1 PNG with its header changed to 20000x20000. Decoding it would
	// fail, so the error must come from the size check that runs first.
	data := encodePNG(t, image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	const ihdr = 8 + 4 + 4 // PNG signature, chunk length, chunk type
	binary.BigEndian.PutUint32(data[ihdr:], 20000)
	binary.BigEndian.PutUint32(data[ihdr+4:], 20000)
	binary.BigEndian.PutUint32(data[ihdr+13:], crc32.ChecksumIEEE(data[ihdr-4:ihdr+13]))
	if _, err := convertAvatarToJPEG(data); !errors.Is(err, errAvatarTooLarge) {
		t.Fatalf("err = %v, want errAvatarTooLarge", err)
	}
}

// withEXIFOrientation inserts an EXIF segment with the orientation tag right
// after the JPEG start marker.
func withEXIFOrientation(jpegData []byte, order binary.ByteOrder, orientation uint16) []byte {
	tiff := make([]byte, 8+2+12+4)
	if order == binary.LittleEndian {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	order.PutUint16(tiff[2:], 42)
	order.PutUint32(tiff[4:], 8)
	order.PutUint16(tiff[8:], 1)
	order.PutUint16(tiff[10:], 0x0112)
	order.PutUint16(tiff[12:], 3) // SHORT
	order.PutUint32(tiff[14:], 1)
	order.PutUint16(tiff[18:], orientation)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	out := append([]byte{}, jpegData[:2]...)
	out = append(out, segment...)
	out = append(out, payload...)
	return append(out, jpegData[2:]...)
}

func encodeTestJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestJPEGOrientation(t *testing.T) {
	plain := encodeTestJPEG(t, solidImage(red))
	for name, tc := range map[string]struct {
		data []byte
		want int
	}{
		"no EXIF":       {plain, 1},
		"little endian": {withEXIFOrientation(plain, binary.LittleEndian, 6), 6},
		"big endian":    {withEXIFOrientation(plain, binary.BigEndian, 8), 8},
		"invalid value": {withEXIFOrientation(plain, binary.BigEndian, 9), 1},
		"not a JPEG":    {[]byte("not a jpeg"), 1},
		"truncated":     {withEXIFOrientation(plain, binary.BigEndian, 6)[:20], 1},
	} {
		if got := jpegOrientation(tc.data); got != tc.want {
			t.Errorf("%s: orientation = %d, want %d", name, got, tc.want)
		}
	}
}

func TestOrientRGBA(t *testing.T) {
	// A 2x1 image: red on the left, blue on the right.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, red)
	src.Set(1, 0, blue)
	for orientation, want := range map[int][]color.RGBA{
		1: {red, blue}, // left to right
		2: {blue, red},
		3: {blue, red},
		4: {red, blue},
		5: {red, blue}, // top to bottom
		6: {red, blue},
		7: {blue, red},
		8: {blue, red},
	} {
		dst := orientRGBA(src, orientation)
		var got []color.RGBA
		if orientation >= 5 {
			if dst.Rect.Dx() != 1 || dst.Rect.Dy() != 2 {
				t.Fatalf("orientation %d: size = %v, want 1x2", orientation, dst.Rect)
			}
			got = []color.RGBA{dst.RGBAAt(0, 0), dst.RGBAAt(0, 1)}
		} else {
			got = []color.RGBA{dst.RGBAAt(0, 0), dst.RGBAAt(1, 0)}
		}
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("orientation %d: pixels = %v, want %v", orientation, got, want)
		}
	}
}

func TestConvertAvatarToJPEGRotatesLargeJPEG(t *testing.T) {
	data := withEXIFOrientation(encodeTestJPEG(t, image.NewRGBA(image.Rect(0, 0, 5000, 100))), binary.BigEndian, 6)
	out, err := convertAvatarToJPEG(data)
	if err != nil {
		t.Fatal(err)
	}
	if bounds := decodeJPEG(t, out).Bounds(); bounds.Dx() != 81 || bounds.Dy() != maxAvatarSide {
		t.Fatalf("size = %dx%d, want 81x%d", bounds.Dx(), bounds.Dy(), maxAvatarSide)
	}
}

// An animated WebP (4x4, two frames) made with Pillow.
const animatedWebP = "UklGRoQAAABXRUJQVlA4WAoAAAACAAAAAwAAAwAAQU5JTQYAAAAAAAAAAABBTk1GKAAAAAAAAAAAAAMAAAMAAGQAAAJWUDhMDwAAAC8DwAAABxD9j/4HIqL/AQBBTk1GKAAAAAAAAAAAAAMAAAMAAGQAAABWUDhMDwAAAC8DwAAABxDR//4HIqL/AQA="

func TestConvertAvatarToJPEGRejectsOtherFormats(t *testing.T) {
	webpData, err := base64.StdEncoding.DecodeString(animatedWebP)
	if err != nil {
		t.Fatal(err)
	}
	var tiffData bytes.Buffer
	if err = tiff.Encode(&tiffData, solidImage(blue), nil); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"webp":         webpData,
		"tiff":         tiffData.Bytes(),
		"not an image": []byte("not an image"),
	} {
		if _, err := convertAvatarToJPEG(data); !errors.Is(err, errAvatarFormat) {
			t.Errorf("%s: err = %v, want errAvatarFormat", name, err)
		}
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
