import { getBundledModel } from "@oh-my-pi/pi-catalog";
import { CURRENT_SETUP_VERSION } from "@oh-my-pi/pi-tui/setup/setup-version";

type CatalogModel = {
  name: string;
  input: unknown;
  cost: unknown;
  contextWindow: number;
  maxTokens: number;
};

function modelFor(source: string): CatalogModel {
  const [provider, rawId] = source.split("/", 2);
  const id = rawId?.replace(/-1m$/, "");
  if (!id || (provider !== "openai" && provider !== "anthropic")) {
    throw new Error(`Unsupported Tsuna source model: ${source}`);
  }
  const family = provider === "openai" ? "openai-codex" : "anthropic";
  const model = getBundledModel(family, id);
  if (!model) throw new Error(`OMP catalog has no ${family}/${id}`);
  if (typeof model.contextWindow !== "number" || typeof model.maxTokens !== "number"
    || !Number.isFinite(model.contextWindow) || !Number.isFinite(model.maxTokens)) {
    throw new Error(`OMP catalog has invalid token limits for ${family}/${id}`);
  }
  return {
    name: model.name,
    input: model.input,
    cost: model.cost,
    contextWindow: model.contextWindow,
    maxTokens: model.maxTokens,
  };
}

const sources = [...new Set(process.argv.slice(2))];
console.log(JSON.stringify({
  setupVersion: CURRENT_SETUP_VERSION,
  models: Object.fromEntries(sources.map(source => [source, modelFor(source)])),
}));
