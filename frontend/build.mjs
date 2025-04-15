import postcss from "postcss"
import postcssRelativeColorSyntax from "@csstools/postcss-relative-color-syntax"
import { build } from "esbuild"
import { readFile, writeFile, rm } from "fs/promises"

const postcssPlugin = ({
    extensions = [".css"],
    plugins = [],
    options = {},
} = {}) => ({
    name: 'postcss',
    async setup(build) {
        build.onLoad({
            filter: new RegExp(`(${extensions.join("|")})$`),
        }, async (args) => {
            const css = await readFile(args.path, "utf8");
            const result = await postcss(plugins).process(css, {
                ...options,
                from: args.path,
            })
            return {
                contents: result.css,
                loader: "css",
            }
        })
    },
})

try {

console.info("> clean build dir")
await rm("build", {
    force: true,
    recursive: true,
})

console.info("> build template")
const { default: render } = await import(`data:text/javascript;base64,${btoa((await build({
    write: false,
    entryPoints: ["frontend/index.jsx"],
    jsx: "transform",
    jsxImportSource: "preact",
    bundle: true,
    platform: "node",
    format: "esm",
    sourcemap: "inline",
})).outputFiles[0].text)}`)

console.info("> build bundle")
const { metafile } = await build({
    metafile: true,
    entryPoints: ["frontend/index.mjs"],
    entryNames: "[dir]/[name]-[hash]",

    bundle: true,
    outfile: "build/bundle.js",

    plugins: [
        postcssPlugin({
            plugins: [
                postcssRelativeColorSyntax,
            ],
        }),
    ],
    loader: {
        ".woff2": "file",
    },
    platform: "browser",
    target: ["chrome94", "firefox94", "safari15"],
})
const bundleJS = Object.entries(metafile.outputs).find(([,{entryPoint}]) => entryPoint == "frontend/index.mjs")[0]
const bundleCSS = metafile.outputs[bundleJS].cssBundle

console.info("> render page")
await writeFile("build/index.html", render({
    bundleJS: bundleJS.slice("build/".length),
    bundleCSS: bundleCSS.slice("build/".length),
}))

console.info("> done")

} catch (ex) { console.error(ex.message) }
