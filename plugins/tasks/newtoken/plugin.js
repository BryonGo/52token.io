// Bundled with the shared Ark driver by plugins.Source. The model suffix is
// part of NewToken's wire identity and is never removed for routing or pricing.
export function configureProvider(driver) {
  const newtokenVideos = {};
  for (const model of ["doubao-seedance-2-0-260128", "doubao-seedance-2-0-fast-260128", "doubao-seedance-2-0-mini-260615", "doubao-seedance-2-5-260628"]) {
    newtokenVideos[model] = Object.assign({}, driver.videoModels[model], { resolutions: ["720p"], defaultResolution: "720p" });
    driver.videoModels[model] = newtokenVideos[model];
  }
  const newtokenImages = {};
  const newtokenSizes = {
    "1k": ["1024x1024", "1152x864", "864x1152", "1424x800", "800x1424", "1248x832", "832x1248", "1568x672"],
    "2k": ["2048x2048", "2368x1776", "1776x2368", "2816x1584", "1584x2816", "2496x1664", "1664x2496", "3136x1344"],
  };
  for (const tier of Object.keys(newtokenSizes)) {
    const model = "dola-seedream-5-0-pro-260628-" + tier;
    const profile = Object.assign({}, driver.imageModels["dola-seedream-5-0-pro-260628"], {
      presets: [tier.toUpperCase()],
      allowedSizes: newtokenSizes[tier],
      defaultSize: newtokenSizes[tier][0],
      urlOnly: true,
      layers: false,
      background: false,
    });
    newtokenImages[model] = profile;
    driver.imageModels[model] = profile;
  }
  driver.configure(
    {
      key: "newtoken",
      name: "NewToken",
      providerKind: "relay",
      icon: "text:NT",
      version: "1.2.0",
      baseUrl: "https://newtoken.club",
      website: "https://doc.newtoken.club/",
      description: { en: "NewToken Seedance native video tasks and Seedream image generation", zh: "NewToken Seedance 原生视频任务与 Seedream 图片生成" },
    },
    newtokenVideos,
    newtokenImages,
    "/v1/images/generations"
  );
  configureNewTokenRelayTasks(driver);
}

// These upstream models use /v1/videos even for asynchronous image jobs.
// Their price unit is synchronized separately; the driver reports quantities.
const NEWTOKEN_SECOND_MODELS = [
  "video-ultra2.5-720p",
  "video-ultra-720p",
  "video-ultra-720p-fast",
  "video-ultra-720p-mini",
  "sd2.0-720p-mini-official",
  "sd2.0-720p-official",
  "sd2.0-1080p-official",
  "sd2.0-720p-fast-official",
  "sd2.5-720p-official",
];
const NEWTOKEN_SYNC_IMAGES = [
  "gpt-image-2",
  "gpt-image-2.5-flare",
  "gpt-image-2.5-sunburst",
  "nana-banana-2_sync",
  "nana-banana-pro_sync",
  "nana-banana-2-4k_sync",
  "nana-banana-pro-4k_sync",
];
const NEWTOKEN_GPT_SIZES = [
  "1024x1024",
  "2048x2048",
  "3072x3072",
  "1280x960",
  "1600x1200",
  "2560x1920",
  "960x1280",
  "1200x1600",
  "1920x2560",
  "1792x1024",
  "1536x864",
  "2560x1440",
  "3840x2160",
  "1024x1792",
  "864x1536",
  "1440x2560",
  "2160x3840",
  "1536x512",
  "3072x1024",
  "4608x1536",
  "512x1536",
  "1024x3072",
  "1536x4608",
];
const NEWTOKEN_ASYNC_IMAGES = ["nana-banana-pro", "nana-banana-2", "nana-banana-2-4k", "nana-banana-pro-4k", "gpt-image2-1k", "gpt-image2-2k", "gpt-image2-4k"];
function newTokenRelayKind(ctx) {
  const model = ctx.upstreamModel || ctx.model;
  if (NEWTOKEN_SECOND_MODELS.includes(model)) return "video";
  if (NEWTOKEN_ASYNC_IMAGES.includes(model) || NEWTOKEN_SYNC_IMAGES.includes(model)) return "image";
  return "";
}
function newTokenRelayRoot(ctx) {
  return (
    String(ctx.baseUrl || "")
      .replace(/\/+$/, "")
      .replace(/(?:\/v1)+$/, "") + (ctx.upstream && ctx.upstream.kind === "new_api" ? "/newtoken" : "")
  );
}
function newTokenArkContext(ctx) {
  return Object.assign({}, ctx, {
    baseUrl: String(ctx.baseUrl || "")
      .replace(/\/+$/, "")
      .replace(/(?:\/v1|\/api\/v3)+$/, ""),
  });
}
function newTokenRelayBody(ctx) {
  if (!ctx.body || ctx.body.kind !== "json" || !ctx.body.value || Array.isArray(ctx.body.value)) throw new Error("JSON object required");
  const req = Object.assign({}, ctx.body.value, { model: ctx.model || ctx.body.value.model });
  if (!newTokenRelayKind({ model: req.model })) throw new Error("unsupported NewToken relay model");
  if (NEWTOKEN_SECOND_MODELS.includes(req.model)) {
    const seconds = newTokenSeconds(req.seconds === undefined ? (req.duration === undefined ? 5 : req.duration) : req.seconds);
    const maximum = req.model.includes("2.5") ? 30 : 15;
    if (!Number.isFinite(seconds) || seconds < 4 || seconds > maximum || !Number.isInteger(seconds))
      throw new Error("seconds is outside the model duration range");
    if (req.extra_videos !== undefined && !Array.isArray(req.extra_videos)) throw new Error("extra_videos must be an array");
    if (Array.isArray(req.extra_videos) && req.extra_videos.length && !req.model.endsWith("-official"))
      throw new Error("this model does not support reference videos");
    req.seconds = seconds;
    req.duration = seconds;
  } else {
    if (req.n !== undefined && req.n !== 1) throw new Error("n must be 1 for NewToken image requests");
    if (req.stream) throw new Error("streaming is not supported by this NewToken image model");
    if (typeof req.prompt !== "string" || !req.prompt.trim()) throw new Error("prompt is required");
    if (req.response_format !== undefined && !["url", "b64_json"].includes(req.response_format)) throw new Error("unsupported response_format");
    const references = req.images === undefined ? req.image : req.images;
    if (
      references !== undefined &&
      (Array.isArray(references)
        ? references.some((x) => typeof x !== "string" || !x.trim()) || references.length > 7
        : typeof references !== "string" || !references.trim())
    )
      throw new Error("invalid reference images");
    if (NEWTOKEN_ASYNC_IMAGES.includes(req.model)) {
      if ((req.seconds !== undefined && req.seconds !== "4" && req.seconds !== 4) || (req.duration !== undefined && req.duration !== 4))
        throw new Error("asynchronous image duration must be 4");
      req.seconds = 4;
      delete req.duration;
      if (req.image !== undefined && req.images === undefined) {
        req.images = Array.isArray(req.image) ? req.image : [req.image];
        delete req.image;
      }
    } else if (req.model.startsWith("gpt-image")) {
      if (req.size !== undefined && !NEWTOKEN_GPT_SIZES.includes(req.size)) throw new Error("unsupported GPT image size");
      if (req.quality !== undefined && !["auto", "low", "medium", "high"].includes(req.quality)) throw new Error("unsupported GPT image quality");
    }
  }
  return { kind: "submit", model: req.model, action: NEWTOKEN_SECOND_MODELS.includes(req.model) ? "text_to_video" : "text_to_image", requestBody: req };
}
function newTokenSeconds(value) {
  if (typeof value === "number") return value;
  if (typeof value !== "string" || !/^\d+(?:\.\d+)?s?$/.test(value.trim())) return NaN;
  return Number(value.trim().replace(/s$/, ""));
}
function newTokenRelayData(body) {
  return body && body.data && typeof body.data === "object" && !Array.isArray(body.data) ? body.data : body || {};
}
function newTokenRelayStatus(body) {
  const data = newTokenRelayData(body);
  const status = String(data.status || body.status || "").toLowerCase();
  const statuses = {
    queued: "QUEUED",
    pending: "QUEUED",
    processing: "IN_PROGRESS",
    in_progress: "IN_PROGRESS",
    running: "IN_PROGRESS",
    completed: "SUCCESS",
    succeeded: "SUCCESS",
    success: "SUCCESS",
    failed: "FAILURE",
    error: "FAILURE",
    canceled: "FAILURE",
    cancelled: "FAILURE",
  };
  return statuses[status] || "UNKNOWN";
}
function newTokenRelayImages(body) {
  const data = newTokenRelayData(body);
  const urls = [],
    encoded = [];
  const entries = [].concat(data.images || [], data.output || [], (data.metadata || {}).result_urls || [], Array.isArray(data.data) ? data.data : []);
  if (data.url || data.image_url || data.b64_json) entries.push(data);
  for (const item of entries) {
    const url = typeof item === "string" ? item : item && (item.url || item.image_url);
    if (typeof url === "string" && url.trim() && !urls.includes(url.trim())) urls.push(url.trim());
    if (item && typeof item.b64_json === "string" && item.b64_json && !encoded.includes(item.b64_json)) encoded.push(item.b64_json);
  }
  if (Math.max(urls.length, encoded.length) > 17) throw new Error("too many output images");
  return Array.from({ length: Math.max(urls.length, encoded.length) }, (_, i) =>
    Object.assign({}, urls[i] ? { url: urls[i] } : {}, encoded[i] ? { b64_json: encoded[i] } : {})
  );
}
function newTokenSeedreamFacts(facts) {
  if (!facts) return facts;
  if (facts.images_up_to_1_5k !== undefined && facts.images_above_1_5k !== undefined) facts.image_count = facts.images_up_to_1_5k + facts.images_above_1_5k;
  if (facts.input_images !== undefined) facts.extra_reference_images = Math.max(0, facts.input_images - 1);
  return facts;
}
function configureNewTokenRelayTasks(driver) {
  const { meta, native, protocols, hooks } = driver;
  const models = NEWTOKEN_SECOND_MODELS.concat(NEWTOKEN_ASYNC_IMAGES, NEWTOKEN_SYNC_IMAGES);
  for (const profile of meta.usageProfiles) {
    if (!profile.models.some((name) => name.startsWith("dola-seedream"))) continue;
    profile.schema.image_count = { type: "number", unit: "count", description: { en: "Image generation unit price", zh: "图片生成单价" } };
    profile.schema.extra_reference_images = { type: "number", unit: "count", description: { en: "Additional reference images", zh: "额外参考图数量" } };
    for (const example of profile.examples || []) newTokenSeedreamFacts(example.facts);
  }
  const arkImageDecode = native.createImage;
  native.createImage = (ctx) => (NEWTOKEN_SYNC_IMAGES.includes(ctx.model || (ctx.body.value || {}).model) ? newTokenRelayBody(ctx) : arkImageDecode(ctx));
  for (const route of meta.routes) if (route.decode === "createImage") route.models = route.models.concat(NEWTOKEN_SYNC_IMAGES);
  meta.models = meta.models.concat(models);
  meta.usageProfiles.push({
    models: NEWTOKEN_SECOND_MODELS,
    schema: {
      seconds: { type: "number", unit: "second", description: { en: "Video generation unit price", zh: "视频生成单价" } },
      video_input: { enum: ["none", "video"], description: { en: "Reference video input", zh: "参考视频输入" } },
    },
  });
  meta.usageProfiles.push({
    models: NEWTOKEN_ASYNC_IMAGES.concat(NEWTOKEN_SYNC_IMAGES),
    schema: {
      image_count: {
        type: "number",
        unit: "count",
        unitLabel: { en: "image", zh: "张" },
        description: { en: "Image generation unit price", zh: "图片生成单价" },
      },
    },
  });
  meta.routes.push({ method: "POST", path: "/newtoken/v1/videos", type: "submit", decode: "createRelayTask", render: "relayCreated", models: models });
  meta.routes.push({ method: "GET", path: "/newtoken/v1/videos/:task_id", type: "query", render: "relayStatus" });
  native.createRelayTask = newTokenRelayBody;
  native.relayCreated = function (ctx, task) {
    return { id: task.task_id, status: "queued" };
  };
  native.relayStatus = function (ctx, task) {
    return task.data;
  };
  for (const binding of meta.protocols) {
    if (binding.name === "openai_responses") binding.models = meta.models.filter((model) => !models.includes(model));
    if (binding.name === "openai_video") binding.models = binding.models.concat(NEWTOKEN_SECOND_MODELS);
    if (binding.name === "openai_image") binding.models = binding.models.concat(NEWTOKEN_ASYNC_IMAGES, NEWTOKEN_SYNC_IMAGES);
  }
  const arkBuild = hooks.buildSubmitRequest;
  hooks.buildSubmitRequest = function (ctx) {
    if (!newTokenRelayKind(ctx)) return arkBuild(newTokenArkContext(ctx));
    const body = Object.assign({}, ctx.requestBody, { model: ctx.upstreamModel || ctx.model });
    if (NEWTOKEN_ASYNC_IMAGES.includes(body.model)) body.seconds = "4";
    return {
      url: newTokenRelayRoot(ctx) + (NEWTOKEN_SYNC_IMAGES.includes(ctx.upstreamModel || ctx.model) ? "/v1/images/generations" : "/v1/videos"),
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + ctx.apiKey },
      body: body,
    };
  };
  const arkSubmit = hooks.parseSubmitResponse;
  hooks.parseSubmitResponse = function (ctx, response) {
    if (!newTokenRelayKind(ctx)) return arkSubmit(ctx, response);
    const data = newTokenRelayData(response.body);
    if (NEWTOKEN_SYNC_IMAGES.includes(ctx.upstreamModel || ctx.model)) {
      const images = newTokenRelayImages(response.body);
      if (response.body.error || !images.length) throw new Error("image generation returned no images");
      if (!ctx.publicTaskId) throw new Error("missing gateway task id");
      return { taskId: ctx.publicTaskId, taskData: response.body, immediate: { status: "SUCCESS", progress: "100%", url: images[0].url || "" } };
    }
    const id = String(data.id || data.task_id || "");
    if (!id) throw new Error("upstream task id is missing");
    const state = newTokenRelayStatus(response.body);
    const result = { taskId: id, taskData: response.body };
    if (state === "SUCCESS" || state === "FAILURE")
      result.immediate = { status: state, progress: "100%", reason: state === "FAILURE" ? "upstream task failed" : "" };
    return result;
  };
  const arkQuery = hooks.buildQueryRequest;
  hooks.buildQueryRequest = function (ctx) {
    if (!newTokenRelayKind(ctx)) return arkQuery(newTokenArkContext(ctx));
    return { url: newTokenRelayRoot(ctx) + "/v1/videos/" + encodeURIComponent(ctx.taskId), method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey } };
  };
  const arkParse = hooks.parseTaskResult;
  hooks.parseTaskResult = function (ctx, body, response) {
    if (!newTokenRelayKind(ctx)) return arkParse(ctx, body, response);
    const status = newTokenRelayStatus(body);
    return {
      status: status,
      progress: status === "SUCCESS" || status === "FAILURE" ? "100%" : "0%",
      reason: status === "FAILURE" ? "upstream task failed" : "",
    };
  };
  const arkUsage = hooks.extractUsage;
  hooks.extractUsage = function (ctx) {
    const kind = newTokenRelayKind(ctx);
    if (!kind) return newTokenSeedreamFacts(arkUsage(ctx));
    if (ctx.usagePurpose === "billing_ratios") return {};
    return kind === "video"
      ? {
          seconds: newTokenSeconds((ctx.requestBody || {}).seconds || (ctx.requestBody || {}).duration || 5),
          video_input: Array.isArray((ctx.requestBody || {}).extra_videos) && ctx.requestBody.extra_videos.length ? "video" : "none",
        }
      : { image_count: 1 };
  };
  const arkComplete = hooks.extractUsageOnComplete;
  hooks.extractUsageOnComplete = function (ctx, result, body) {
    const kind = newTokenRelayKind(ctx);
    if (!kind) return newTokenSeedreamFacts(arkComplete(ctx, result, body));
    if (!NEWTOKEN_SYNC_IMAGES.includes(ctx.upstreamModel || ctx.model) && newTokenRelayStatus(body) !== "SUCCESS") return {};
    if (kind === "image") return { image_count: Math.max(newTokenRelayImages(body).length, 1) };
    const data = newTokenRelayData(body);
    const usage = data.usage || {};
    const raw = usage.seconds === undefined ? (data.seconds === undefined ? data.duration : data.seconds) : usage.seconds;
    if (raw === undefined || raw === null) return {};
    const seconds = newTokenSeconds(raw);
    if (!Number.isFinite(seconds) || seconds < 0 || seconds > 3600) throw new Error("invalid upstream billable seconds");
    return { seconds: seconds };
  };
  const arkArtifacts = hooks.listArtifacts;
  hooks.listArtifacts = function (ctx) {
    if (!newTokenRelayKind(ctx)) return arkArtifacts(ctx);
    if (ctx.status !== "SUCCESS") return [];
    if (newTokenRelayKind(ctx) === "image")
      return newTokenRelayImages(ctx.data)
        .filter((item) => item.url)
        .map((item, index) => ({ key: "image-" + (index + 1), type: "image" }));
    const data = newTokenRelayData(ctx.data);
    return data.url || data.video_url || (data.content || {}).video_url ? [{ key: "video", type: "video" }] : [];
  };
  const arkContent = hooks.buildContentRequest;
  hooks.buildContentRequest = function (ctx) {
    if (!newTokenRelayKind(ctx)) return arkContent(ctx);
    const data = newTokenRelayData(ctx.data);
    let url = data.url || data.video_url || (data.content || {}).video_url;
    if (newTokenRelayKind(ctx) === "image") url = (newTokenRelayImages(ctx.data)[Number(String(ctx.artifactKey).replace("image-", "")) - 1] || {}).url;
    if (!url) throw new Error("artifact_not_found");
    return { url: url, method: ctx.clientRequest.method, credentialless: true };
  };
  const videoDecode = protocols.openai_video.decodeRequest;
  protocols.openai_video.decodeRequest = function (ctx) {
    return newTokenRelayKind(ctx) ? newTokenRelayBody(ctx) : videoDecode(ctx);
  };
  const imageDecode = protocols.openai_image.decodeRequest;
  protocols.openai_image.decodeRequest = function (ctx) {
    return newTokenRelayKind(ctx) ? newTokenRelayBody(ctx) : imageDecode(ctx);
  };
  const imageRender = protocols.openai_image.render;
  protocols.openai_image.render = function (ctx, task) {
    return newTokenRelayKind(ctx) ? { data: newTokenRelayImages(task.data) } : imageRender(ctx, task);
  };
}
