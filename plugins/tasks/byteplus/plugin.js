// Bundled with the shared Ark driver by plugins.Source; the served/downloaded
// source is standalone and needs no module imports.
export function configureProvider(driver) {
  const byteplusVideos = {};
  for (const model of Object.keys(driver.videoModels).filter((name) => !name.startsWith("doubao-"))) byteplusVideos[model] = driver.videoModels[model];
  const byteplusImages = {};
  for (const model of Object.keys(driver.imageModels).filter((name) => !name.startsWith("doubao-"))) byteplusImages[model] = driver.imageModels[model];
  driver.configure(
    {
      key: "byteplus",
      name: "BytePlus",
      icon: "text:BP",
      version: "1.0.0",
      baseUrl: "https://ark.ap-southeast.bytepluses.com",
      website: "https://docs.byteplus.com/en/docs/modelark",
      description: { en: "BytePlus ModelArk Seedance video and Seedream image generation", zh: "BytePlus 国际版 Seedance 视频与 Seedream 图片生成" },
    },
    byteplusVideos,
    byteplusImages,
    "/api/v3/images/generations"
  );
}
