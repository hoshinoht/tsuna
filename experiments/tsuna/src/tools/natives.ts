/**
 * Tsuna adapter over `@oh-my-pi/pi-natives@18.8.6` (MIT; Can Bölük, Stencil Labs).
 *
 * The loader prunes old version directories under its natives cache after
 * every load (`~/.omp/natives` by default). Before the first import we point
 * `PI_NATIVES_DIR` at Tsuna-owned state so no shared location is touched, and
 * verify the loaded build matches the pinned release.
 */
import { mkdirSync } from "node:fs";

export const PINNED_NATIVES_VERSION = "18.8.6";

type Natives = typeof import("@oh-my-pi/pi-natives");
let loaded: Promise<Natives> | undefined;
let loadedDir: string | undefined;

export function loadNatives(nativesDir: string): Promise<Natives> {
	if (loaded) {
		if (loadedDir !== nativesDir) {
			// The addon is process-global; a second location cannot take effect.
			return loaded;
		}
		return loaded;
	}
	mkdirSync(nativesDir, { recursive: true, mode: 0o700 });
	process.env.PI_NATIVES_DIR = nativesDir;
	loadedDir = nativesDir;
	loaded = import("@oh-my-pi/pi-natives").then(mod => {
		const version = mod.__piNativesBuildVersion?.();
		if (version !== PINNED_NATIVES_VERSION) {
			throw new Error(`pi-natives build ${version} does not match pinned ${PINNED_NATIVES_VERSION}`);
		}
		return mod;
	});
	return loaded;
}

export function nativesDirInUse(): string | undefined {
	return loadedDir;
}
