# Antigravity Telegram Gateway (Go Version)

*[Read in English](../README.md)*
Bot Telegram viết bằng Golang, đóng vai trò làm cầu nối gateway cho [Antigravity CLI](https://dev.to/arindam_1729/antigravity-cli-a-hands-on-guide-to-googles-terminal-coding-agent-5bc7). Phiên bản này **hoạt động độc lập**, không sử dụng các thư viện API bên thứ 3 nhằm đảm bảo an toàn bảo mật, trực tiếp giao tiếp với Telegram bằng HTTP RAW.

Tài liệu này hướng dẫn chi tiết cách cấu hình Telegram Bot từ BotFather, cài đặt, bảo mật và vận hành bot 24/7.

> [!CAUTION]
> **Cảnh báo Bảo mật**: Việc chạy Antigravity CLI đồng nghĩa với việc xác thực bằng tài khoản Google cá nhân của bạn. Triển khai bot này trên một máy chủ web công khai, dùng chung hoặc bảo mật kém tiềm ẩn rủi ro rất lớn. Nếu máy chủ bị hacker chiếm quyền điều khiển, họ có thể sử tìm cách để truy cập vào các dữ liệu và dịch vụ Google liên kết với tài khoản của bạn.
> **Khuyến nghị**: Chỉ nên triển khai hệ thống này trên các môi trường an toàn, riêng tư như máy cá nhân, home server riêng, hoặc các máy chủ nội bộ chuyên biệt không mở port ra internet công cộng. Nên sử dụng email tài khoản Google cho dịch vụ AI tách biệt khỏi tài khoản chứa dữ liệu cá nhân nhạy cảm hoặc email công việc chính.

---

## ❓ Vì sao chọn Antigravity & Telegram?
[Antigravity CLI](https://dev.to/arindam_1729/antigravity-cli-a-hands-on-guide-to-googles-terminal-coding-agent-5bc7) cung cấp một AI coding agent hoạt động trên terminal cực kỳ mạnh mẽ. Tuy nhiên, để sử dụng nó, bạn thường phải ngồi trước máy tính và tương tác với giao diện dòng lệnh.

Bằng cách kết nối Antigravity với **Telegram**, dự án này mang lại những lợi ích đột phá:
- **Điều khiển AI Agent từ mọi nơi**: Bạn có thể ra lệnh cho agent viết code, quản trị server, hay chạy các tác vụ nền trực tiếp từ điện thoại di động khi đang đi trên đường.
- **Giao diện siêu nhẹ**: Không cần cài đặt các IDE nặng nề hay ứng dụng SSH trên điện thoại. Ứng dụng Telegram có sẵn cung cấp một giao diện UI cực nhanh, quen thuộc và dễ tiếp cận.
- **Tự động hóa 24/7**: Khi được triển khai trên home server hoặc VPS, agent của bạn luôn sẵn sàng 24/7 để nhận lệnh và báo cáo kết quả trực tiếp vào khung chat của bạn.
- **Tận dụng tối đa gói cước Google AI**: Vì Antigravity CLI xác thực trực tiếp qua tài khoản Google của bạn, bạn có thể khai thác triệt để các gói dịch vụ AI đã đăng ký (như Gemini Advanced/Ultra) mà không cần phải trả thêm tiền cho các dịch vụ API đắt đỏ của bên thứ ba. Hơn nữa, so với việc dùng các công cụ như OpenClaw đôi khi có nguy cơ bị khóa do lạm dụng API, việc chạy CLI chính hãng là hoàn toàn hợp lệ và an toàn.

---

## 🌟 Tính năng nổi bật
- **Bảo mật tuyệt đối**: Gọi API trực tiếp qua `net/http` không qua các wrapper tiềm ẩn rủi ro.
- **Bảo mật Sandbox 🛡️**: Giới hạn AI chỉ được phép đọc/ghi file trong các thư mục được bạn cấp phép trước (`ALLOWED_DIRS`).
- **Quản lý Quyền Truy Cập (Whitelist)**: Chỉ phục vụ các User hoặc Chat Group có ID được cấu hình trước.
- **Cơ chế chống đụng độ (Concurrency Lock)**: Tránh spam bot, đảm bảo mỗi người dùng chỉ chạy 1 luồng chính 1 lúc.
- **Hỗ trợ tin nhắn dài**: Tự động chia nhỏ (chunk) khi AI phản hồi dài hơn giới hạn 4096 ký tự của Telegram.
- **Slash Commands**:
  - `/new`: Khởi tạo hội thoại mới tinh.
  - `/dir <path>`: Thay đổi thư mục làm việc (Sandbox).
  - `/goal <prompt>`: Chạy ngầm nhiệm vụ tự động hoàn toàn.
  - `/grill_me <prompt>`: Ép bot hỏi ngược lại để làm rõ ý tưởng.
  - `/browser <prompt>`: Ép bot dùng trình duyệt.
  - `/schedule <time> <prompt>`: Hẹn giờ thực thi lệnh.

---

## 🛠 Phần 1: Cài đặt Bot trên Telegram với BotFather

Để tạo bot trên Telegram, bạn không cần dùng trang web, mọi thứ diễn ra ngay trong ứng dụng Telegram thông qua **BotFather**.

### Bước 1: Tạo Bot và lấy Token
1. Mở ứng dụng Telegram, tìm kiếm tài khoản **@BotFather** (có tích xanh) và bấm Start.
![](assets/botfather_start.png)
2. Gõ lệnh `/newbot` để tạo một bot mới.
![](assets/botfather_newbot.png)
3. BotFather sẽ hỏi bạn tên của bot (ví dụ: `Antigravity Bot`).
4. Tiếp theo, nhập username cho bot (phải kết thúc bằng chữ `bot`, ví dụ: `my_antigravity_bot`).
5. Nếu thành công, BotFather sẽ gửi cho bạn một mã **HTTP API Token** (có dạng `123456789:ABCDefghIJKLmnopQRSTuvwxyz`).
   👉 **Copy và lưu mã này lại**, đây chính là `TELEGRAM_BOT_TOKEN` bạn cần nhập vào file `.env`.

### Bước 2: Thiết lập quyền riêng tư (Bắt buộc nếu cho vào nhóm)
Nếu bạn dự định đưa bot vào Group Chat và muốn nó đọc được **tất cả** tin nhắn (không cần gõ `/` hoặc tag tên bot):
1. Gõ lệnh `/setprivacy` gửi cho BotFather.
2. Chọn bot của bạn ở menu hiện ra.
3. Chọn **Disable**. (Trạng thái `Privacy mode is disabled` có nghĩa là bot sẽ thấy toàn bộ tin nhắn trong nhóm).

### Bước 3: Lấy User ID của bạn
Telegram bảo mật bằng User ID (chuỗi số). Bạn cần ID này để cấu hình Whitelist.
1. Tìm kiếm bot **@userinfobot** trên Telegram và bấm Start.
2. Nó sẽ trả về cho bạn ID của bạn (Ví dụ: `Id: 123456789`).
   👉 **Copy dãy số này** để cấu hình `ALLOWED_USER_IDS` trong file `.env`.
	![](assets/userinfobot_id.png)

*(Lưu ý: Nếu bạn add bot vào một Group chat, có 2 cách để lấy chính xác Chat ID của nhóm:
- **Cách 1 (Dùng Telegram Web):** Mở nhóm trên trình duyệt (Telegram Web), nhìn lên thanh địa chỉ URL. Nếu link có dạng `https://web.telegram.org/k/#-1234567890` thì Chat ID của nhóm thường là chuỗi số đó thêm tiền tố `-100` vào phía trước (Ví dụ: `-1001234567890`).
- **Cách 2 (Chuẩn nhất - Xem qua Log Terminal):** Chạy bot trên Terminal, chat một câu bất kỳ vào nhóm. Nhìn lên màn hình Terminal sẽ thấy dòng chữ: `Received message from User XXX in Chat -1001234567890`. Hãy copy nguyên con số đó để dùng!)*

---

## 💻 Phần 2: Cài đặt & Cấu hình Code

### 1. Chuẩn bị môi trường
- Cài đặt Go (phiên bản 1.21 trở lên).
- Phải cài đặt sẵn [Antigravity CLI](https://dev.to/arindam_1729/antigravity-cli-a-hands-on-guide-to-googles-terminal-coding-agent-5bc7). Lệnh `agy` cần có sẵn trong `$PATH`.

### 2. Cấu hình
Mở terminal tại thư mục gốc và thực hiện:
```bash
# Di chuyển vào thư mục dự án
cd antigravity-cli-telegram

# Khởi tạo các thư viện (chỉ dùng godotenv)
go mod tidy

# Copy file cấu hình
cp .env.example .env
```

Mở file `.env` bằng `nano .env` và cấu hình:
```ini
TELEGRAM_BOT_TOKEN=123456789:YOUR_TOKEN_HERE
ALLOWED_USER_IDS=123456789
ALLOWED_CHAT_IDS=
ANTIGRAVITY_CMD=agy
ALLOWED_DIRS=/path/to/your/projects,/var/www/html
```

---

## 🚀 Phần 3: Chạy Bot 24/7 trên Linux VPS

Chúng ta sẽ dùng **systemd** để giữ cho bot luôn chạy ngầm và tự khởi động lại.

### Deploy lần đầu tiên
1. Từ thư mục gốc của dự án (`antigravity-cli-telegram`), build ra file chạy:
   ```bash
   go build .
   ```
2. Mở file `antigravity-bot-tg.service` (đã có sẵn trong thư mục) và kiểm tra/sửa lại đường dẫn `WorkingDirectory` và `ExecStart` cho đúng với thư mục thực tế trên VPS của bạn (mặc định đang là `/path/to/your/projects/antigravity-cli-telegram`).
3. Cài đặt và khởi chạy dịch vụ:
   ```bash
   sudo cp antigravity-bot-tg.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable antigravity-bot-tg --now
   ```
4. Kiểm tra trạng thái xem bot đã online chưa:
   ```bash
   sudo systemctl status antigravity-bot-tg
   ```
   *(Để xem log theo thời gian thực: `sudo journalctl -u antigravity-bot-tg -f`)*

### Cập nhật khi có thay đổi code
Khi có phiên bản mới hoặc thay đổi code trên Github, bạn chỉ cần thực hiện 3 bước đơn giản trên VPS:
```bash
# 1. Kéo code mới nhất về
git pull

# 2. Build lại file thực thi
cd antigravity-cli-telegram
go build -o antigravity-cli-telegram .

# 3. Khởi động lại service
sudo systemctl restart antigravity-bot-tg
```

---

## 💡 Phần 4: Cách sử dụng

### Quy tắc Tương tác (Rất Quan Trọng)
Để tránh việc bot gửi toàn bộ câu chuyện phiếm trong nhóm lên AI làm tốn kém chi phí API và làm nhiễu ngữ cảnh, bot được thiết kế với màng lọc tin nhắn cực kỳ thông minh:

1. **Trong tin nhắn riêng (Direct Message / Inbox 1-1)**:
   - **Không cần tag**. Bạn cứ chat bình thường, bot sẽ nhận và phản hồi mọi tin nhắn hợp lệ.
2. **Trong Group Chat (Nhóm / Siêu Nhóm)**:
   - **Tin nhắn chữ bình thường**: BẠN **BẮT BUỘC PHẢI TAG** tên bot (ví dụ: `@tên_bot code cho tao file abc`). Mã nguồn sẽ tự động tách thẻ tag ra trước khi nạp vào AI. Nếu không có tag, bot sẽ chủ động ngó lơ tin nhắn.
   - **Lệnh hệ thống (Slash Commands)**: Nếu bạn dùng lệnh bắt đầu bằng dấu gạch chéo (`/new`, `/goal`, ...), bot sẽ luôn nhận lệnh ngay lập tức **mà không cần phải tag tên**.

### Hướng dẫn sử dụng lệnh
- Bạn có thể gửi lệnh trực tiếp như `/goal viết API server` hoặc gõ dấu `/` để chọn lệnh (Bot sẽ tự động hiện menu gợi ý vì đã được cấu hình qua API khi khởi động).
- Do giới hạn định dạng lệnh của Telegram, một số lệnh cần dùng dấu gạch dưới (ví dụ: `/grill_me`).

## 🛠️ Xử lý sự cố
- **Bot không phản hồi, systemctl báo lỗi không tìm thấy `agy`**: Lỗi biến môi trường PATH. Trong file `antigravity-bot-tg.service`, bạn hãy kiểm tra lại dòng `Environment="PATH=..."` xem đã bao gồm thư mục chứa file chạy `agy` chưa (thường là `~/.local/bin` hoặc `~/go/bin`).
- **Lỗi `Unauthorized message ignored` trong Log**: Nghĩa là ID Telegram của bạn hoặc của Group Chat không khớp với thiết lập trong file `.env`. Xem lại bước lấy User ID ở phần 1.
