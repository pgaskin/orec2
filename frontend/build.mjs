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
    await rm("build", {
        force: true,
        recursive: true,
    })

    const { metafile } = await build({
        metafile: true,
        entryPoints: ["frontend/index.mjs"],
        entryNames: "[dir]/[name]-[hash]",

        bundle: true,
        outfile: "build/bundle.js",
        /*
        outdir: "build",
        splitting: true,
        format: "esm",
        */

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
        target: ["chrome94", "firefox94", "safari15"],
    })

    const html = (await readFile("frontend/index.html", { encoding: "utf-8" }))
        .replaceAll("bundle.js", Object.keys(metafile.outputs).find(x => x.startsWith("build/bundle-") && x.endsWith(".js")).split("/").pop())
        .replaceAll("bundle.css", Object.keys(metafile.outputs).find(x => x.startsWith("build/bundle-") && x.endsWith(".css")).split("/").pop())

    await writeFile("build/index.html", html)
} catch (e) {
    console.error(e.message)
}
