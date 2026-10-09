export async function request(path, {method = "GET", body, token, idempotencyKey} = {}) {
  const headers = {Accept: "application/json"};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers.Authorization = `Bearer ${token}`;
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 15000);
  try {
    const response = await fetch(path, {method, headers, credentials: "same-origin", signal: controller.signal, body: body === undefined ? undefined : JSON.stringify(body)});
    const value = response.status === 204 ? null : await response.json().catch(() => null);
    if (!response.ok) {
      const messages = {400: "Проверьте заполненные поля.", 401: "Неверный токен администратора.", 403: "Запрос отклонён. Для голосования разрешите cookie и обновите страницу.", 404: "Опрос не найден. Проверьте ссылку из эфира.", 409: "Голосование ещё не началось или уже завершилось.", 429: "Слишком много запросов. Повторите через несколько секунд.", 503: "Сервис временно занят. Попробуйте ещё раз."};
      const codes = {idempotency_conflict: "Этот ключ создания уже использован для другого опроса. Измените форму перед повторной отправкой.", poll_closed: "Голосование ещё не началось или уже завершилось.", incomplete_results: "Результаты пока недоступны полностью. Попробуйте обновить их позже.", identity_required: "Для голосования разрешите cookie и обновите страницу.", cross_origin: "Ссылка не соответствует адресу сервиса. Откройте страницу по официальной ссылке.", invalid_choices: "Проверьте количество выбранных вариантов.", invalid_poll: "Проверьте вопрос, варианты и время голосования. Опрос может длиться до 24 часов.", outcome_unknown: "Подтверждение задержалось. Повторите отправку — ваш голос будет учтён один раз."};
      const code = value?.code || value?.error?.code || value?.error;
      const translated = typeof code === "string" && Object.hasOwn(codes, code) ? codes[code] : undefined;
      const error = new Error(translated || messages[response.status] || "Сервис не смог обработать запрос. Попробуйте ещё раз.");
      error.status = response.status;
      error.retryAfter = Math.min(10, Math.max(1, Number(response.headers.get("Retry-After")) || 1));
      throw error;
    }
    return {status: response.status, value};
  } catch (error) {
    if (error.name === "AbortError") throw new Error("Ответ не получен. Повторите запрос с той же страницы.");
    if (error instanceof TypeError) throw new Error("Не удалось соединиться с сервисом. Проверьте интернет и повторите запрос.");
    throw error;
  } finally { clearTimeout(timer); }
}

export function message(element, text = "", kind = "error") {
  element.textContent = text;
  element.className = `message ${kind}`;
}

export function pollState(poll) {
  const now = Date.now();
  if (now < Date.parse(poll.opens_at)) return {text: "Скоро в эфире", kind: "future", open: false};
  if (now >= Date.parse(poll.closes_at)) return {text: "Завершён", kind: "closed", open: false};
  return {text: "В эфире", kind: "live", open: true};
}

export function dateTime(value) {
  return new Intl.DateTimeFormat("ru-RU", {day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit"}).format(new Date(value));
}

export function element(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}
