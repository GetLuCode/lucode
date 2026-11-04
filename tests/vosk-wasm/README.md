# Vosk wasm (small-es) — playground y pruebas

Prueba de `vosk-browser` 0.0.8 (wasm ya compilado) con `vosk-model-small-es-0.42`, sin lucode web.

## Preparar
```
npm install
cp node_modules/vosk-browser/dist/vosk.js vosk.js
# modelo: carpeta vosk-model-small-es-0.42 -> tar.gz con la carpeta dentro
tar -czf model-small-es.tar.gz vosk-model-small-es-0.42
```

## Playground (JS puro)
`node serve.mjs` (PORT, default 8138) -> http://127.0.0.1:8138/
- Modo Comandos: grupos de frases, gramatica + `[unk]`, frase inicial opcional, verificacion con dictado libre (tolerancia ~15 % de edicion).
- Modo Habla continua: detecta el wake con gramatica, corta el audio desde el wake hasta el fin del dictado libre y lo muestra para escuchar.

## Autopruebas (Chrome headless, sin clics)
- `node run.mjs a.wav b.wav` (GRAMMAR='[...]' opcional): decodifica wavs.
- `node test-playground.mjs`, `node test-modos.mjs`, `node test-wake.mjs wake_frase.wav sin_wake.wav`.
