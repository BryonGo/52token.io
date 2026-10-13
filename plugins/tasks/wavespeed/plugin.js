// WaveSpeed AI 视频任务渠道（渠道类型 61 = Task Plugin）。
//
// 上游是 WaveSpeed 自家的一套「提交 → 轮询」私有协议（不是 OpenAI 视频 API，
// 也不是 ARK / Seedance 协议）：
//
//   POST {base}/api/v3/{model_id}
//     {"prompt":"…","image":"https://…","resolution":"480p","duration":5,"seed":0}
//     → {"code":200,"data":{"id":"pred_…","status":"created","urls":{"get":"…"}}}
//   GET  {base}/api/v3/predictions/{id}/result
//     → {"code":200,"data":{"status":"completed","outputs":["https://….mp4"]}}
//
// 状态：created / processing / completed / failed（另有 cancelled/timeout/deleted）。
//
// 所以本插件只声明宿主协议 `openai_video`（客户端照常打 /v1/videos），
// 由 buildSubmitRequest / buildQueryRequest 把两侧字段对齐。
//
// ⚠️ 素材：WaveSpeed 只收**图片 URL**（image）。接口层面不收 base64/文件部件。
//    本插件对 URL 原样透传；对上传的文件按 data URL 内联（encoding:"dataUrl"）——
//    上游是否吃 data URI **未经实测**，若被上游以 400 拒绝，走「文件→URL」请改经
//    LiteLLM/arkv 渠道（arkv 会先上传到 /api/v3/media/uploads 换成 download_url）。
//
// 计费：上游按「分辨率 × 时长」线性计价（480p = $0.03/秒、720p = $0.06/秒），
//       故 usageSchema 同时给出 seconds 与 resolution 两个计费维度。

export const meta = {
  apiVersion: 1,
  key: "wavespeed",
  name: "WaveSpeed",
  description: {
    en: "WaveSpeedAI video generation (image-to-video)",
    zh: "WaveSpeedAI 视频生成（图生视频）",
  },
  version: "1.0.0",
  author: { name: "avmaker" },
  baseUrl: "https://api.wavespeed.ai",
  allowedHosts: ["api.wavespeed.ai"],
  // 公开别名与上游 model_id 都列上：渠道既可用别名映射、也可直接用上游全名。
  models: ["wavespeed-wan-2.2-spicy-i2v", "wavespeed-ai/wan-2.2-spicy/image-to-video"],
  fetchMode: "per_task",
  protocols: ["openai_video"],
  usageSchema: {
    seconds: {
      type: "number",
      unit: "second",
      description: { en: "Output video duration in seconds", zh: "输出视频时长（秒）" },
    },
    // 上游单价按分辨率分档，故分辨率也是计费维度。
    resolution: {
      enum: ["480p", "720p"],
      enumLabels: { "480p": { en: "480p", zh: "480p" }, "720p": { en: "720p", zh: "720p" } },
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
  },
  usageExamples: [
    { label: "480p · 5s", facts: { seconds: 5, resolution: "480p" } },
    { label: "720p · 5s", facts: { seconds: 5, resolution: "720p" } },
    { label: "480p · 8s", facts: { seconds: 8, resolution: "480p" } },
    { label: "720p · 8s", facts: { seconds: 8, resolution: "720p" } },
  ],
};

// 上游 model_id（本插件唯一现役模型）。
const VENDOR_MODEL = "wavespeed-ai/wan-2.2-spicy/image-to-video";
// 上游文档：resolution 默认 480p，duration 默认 5。
const DEFAULT_RESOLUTION = "480p";
const DEFAULT_DURATION = 5;

function trimmed(value) {
  return String(value === undefined || value === null ? "" : value).trim();
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

// size（"1280x720"）→ 上游档位（按**短边**定档，竖版 720x1280 也要判成 720p）。
function resolutionFromSize(size) {
  const match = /^\s*(\d+)\s*x\s*(\d+)\s*$/i.exec(String(size || ""));
  if (!match) return "";
  const w = Number(match[1]);
  const h = Number(match[2]);
  if (!w || !h) return "";
  const short = Math.min(w, h);
  if (short >= 720) return "720p";
  if (short >= 480) return "480p";
  return "";
}

// 把「要发给上游的 model_id」定下来：
//  · 渠道配了 model_mapping（upstreamModel ≠ 请求里的 model）→ 用映射值；
//  · 请求的 model 本身就是带 `/` 的上游全名 → 直接用；
//  · 其余（只有公开别名、没配映射）→ 回落到本插件唯一的上游 model_id。
function resolveUpstreamModel(ctx) {
  const up = trimmed(ctx && ctx.upstreamModel);
  const pub = trimmed(ctx && ctx.model);
  if (up && up !== pub) return up;
  if (up && up.indexOf("/") >= 0) return up;
  return VENDOR_MODEL;
}

// 从 openai_video 请求里取图片：input_reference 优先，其次 image。
// 文件部件（ctx.body.kind === "multipart"）由 decodeRequest 转成占位对象。
function imagePlaceholder(fileRef) {
  return { __fileRef: fileRef, encoding: "dataUrl" };
}

function firstOutputURL(outputs) {
  if (!Array.isArray(outputs)) return "";
  for (const item of outputs) {
    if (typeof item === "string" && trimmed(item)) return trimmed(item);
    if (isPlainObject(item)) {
      const url = trimmed(item.url) || trimmed(item.video_url) || trimmed(item.result_url) || trimmed(item.download_url);
      if (url) return url;
    }
  }
  return "";
}

// 归一后的 requestBody（decodeRequest 产出，buildSubmitRequest 消费）。
function normalizeRequest(req, image) {
  const body = { prompt: trimmed(req.prompt), image: image };
  const resolution = trimmed(req.resolution) || resolutionFromSize(req.size);
  if (resolution) body.resolution = resolution;
  const duration = req.duration !== undefined && req.duration !== null && req.duration !== "" ? Number(req.duration) : undefined;
  const seconds = req.seconds !== undefined && req.seconds !== null && req.seconds !== "" ? Number(req.seconds) : undefined;
  const length = Number.isFinite(duration) ? duration : seconds;
  if (Number.isFinite(length) && length > 0) body.duration = length;
  if (req.seed !== undefined && req.seed !== null && req.seed !== "") body.seed = Number(req.seed);
  return body;
}

export function buildSubmitRequest(ctx) {
  const body = ctx.requestBody || {};
  const model = resolveUpstreamModel(ctx);
  const payload = { prompt: trimmed(body.prompt), image: body.image };
  if (body.resolution) payload.resolution = body.resolution;
  if (body.duration !== undefined) payload.duration = body.duration;
  if (body.seed !== undefined) payload.seed = body.seed;
  if (isPlainObject(payload.image) && payload.image.__fileRef) {
    // 宿主会把 FilePlaceholder 就地换成 data URL 字符串。
    payload.image = { __fileRef: payload.image.__fileRef, encoding: "dataUrl" };
  }
  if (!trimmed(payload.image)) throw new Error("image is required (image-to-video)");
  return {
    url: ctx.baseUrl + "/api/v3/" + model,
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      Authorization: "Bearer " + ctx.apiKey,
    },
    body: payload,
    action: "image_to_video",
  };
}

export function parseSubmitResponse(ctx, resp) {
  const result = resp.body || {};
  const code = Number(result.code);
  if (Number.isFinite(code) && code !== 200 && code !== 0) throw new Error(result.message || "wavespeed submit failed");
  const id = trimmed(result.data && result.data.id);
  if (!id) throw new Error("missing prediction id");
  return { taskId: id, taskData: result };
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/api/v3/predictions/" + encodeURIComponent(ctx.taskId) + "/result",
    method: "GET",
    headers: { Accept: "application/json", Authorization: "Bearer " + ctx.apiKey },
  };
}

const TASK_STATUS = {
  created: "QUEUED",
  pending: "QUEUED",
  queued: "QUEUED",
  processing: "IN_PROGRESS",
  running: "IN_PROGRESS",
  in_progress: "IN_PROGRESS",
  completed: "SUCCESS",
  succeeded: "SUCCESS",
  success: "SUCCESS",
  failed: "FAILURE",
  error: "FAILURE",
  cancelled: "FAILURE",
  canceled: "FAILURE",
  timeout: "FAILURE",
  deleted: "FAILURE",
};

function failureReason(data, body) {
  if (isPlainObject(data.error)) {
    const message = trimmed(data.error.message) || trimmed(data.error.code);
    if (message) return message;
  }
  if (typeof data.error === "string" && trimmed(data.error)) return trimmed(data.error);
  return trimmed(data.message) || trimmed(body && body.message) || "task failed";
}

export function parseTaskResult(ctx, body) {
  const data = (body && body.data) || {};
  const raw = trimmed(data.status).toLowerCase();
  const status = TASK_STATUS[raw];
  if (!status) return { status: "UNKNOWN", reason: "unknown task status: " + raw };
  const result = { taskId: ctx.taskId, status: status };
  if (status === "FAILURE") result.reason = failureReason(data, body);
  if (status === "SUCCESS") {
    const url = firstOutputURL(data.outputs);
    if (url) result.url = url;
  }
  return result;
}

// 计费事实：上游按秒 × 分辨率分档计价。
export function extractUsage(ctx) {
  if (ctx.usagePurpose === "billing_ratios") return null;
  const req = ctx.requestBody || {};
  const seconds = Number.isFinite(Number(req.duration)) && Number(req.duration) > 0 ? Number(req.duration) : DEFAULT_DURATION;
  return { seconds: seconds, resolution: trimmed(req.resolution) || DEFAULT_RESOLUTION };
}

function taskData(ctx) {
  const data = (ctx && ctx.data) || {};
  return isPlainObject(data.data) ? data.data : data;
}

function videoURL(ctx) {
  return firstOutputURL(taskData(ctx).outputs);
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" && videoURL(task) ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  const url = videoURL(ctx);
  if (!url) throw new Error("artifact_not_found");
  // 产物是 WaveSpeed 的预签名 URL，公开可读，无需带渠道凭据。
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) {
        throw new Error("JSON or multipart body required");
      }
      let req;
      let image = "";
      if (ctx.body.kind === "json") {
        if (!isPlainObject(ctx.body.value)) throw new Error("JSON object required");
        req = ctx.body.value;
      } else {
        req = {};
        const fields = ctx.body.fields || {};
        for (const name of Object.keys(fields)) {
          if (fields[name].length > 1) throw new Error(name + " must be provided once");
          req[name] = fields[name][0];
        }
        const files = ctx.body.files || [];
        for (const file of files) {
          if (file.field !== "input_reference") throw new Error("unexpected file field: " + file.field);
        }
        if (files.length > 1) throw new Error("input_reference must be provided once");
        // 文件：交给宿主内联成 data URL（上游是否接受见文件头注释）。
        if (files.length) image = imagePlaceholder(files[0].ref);
      }
      if (!image) {
        const candidate = req.input_reference !== undefined && req.input_reference !== null ? req.input_reference : req.image;
        if (isPlainObject(candidate) && candidate.__fileRef) image = imagePlaceholder(candidate.__fileRef);
        else if (trimmed(candidate)) image = trimmed(candidate);
      }
      const prompt = trimmed(req.prompt);
      if (!prompt) throw new Error("prompt is required");
      if (!trimmed(image)) throw new Error("image is required (image-to-video)");
      return {
        kind: "submit",
        model: ctx.model,
        action: "image_to_video",
        requestBody: normalizeRequest(req, image),
      };
    },
    // 宿主负责 id/object/model/status/progress/created_at/completed_at；这里只补扩展字段。
    render: function (ctx, task) {
      const data = taskData({ data: (task && task.data) || {} });
      const output = { object: "video" };
      const seconds = Number(data.duration);
      if (Number.isFinite(seconds) && seconds > 0) output.seconds = String(seconds);
      if (task && task.status === "FAILURE") {
        output.error = { code: "video_generation_failed", message: task.fail_reason || "task failed" };
      }
      return output;
    },
  },
};
