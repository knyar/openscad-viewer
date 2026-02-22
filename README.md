# openscad-viewer

A web-based OpenSCAD viewer with 
[arcball controls](https://threejs.org/docs/#ArcballControls), useful for previewing
OpenSCAD models on a device with touchscreen display.

Uses a local OpenSCAD binary to convert the model to the
[OFF](https://en.wikipedia.org/wiki/OFF_(file_format)) format, and then displays
it using three.js.

The web viewer reloads the model if any of the files in the same directory
as the .scad file are changed, assuming all dependencies are in the same dir.

## Usage

```bash
go run . /path/to/openscad /path/to/myfile.scad
```

Then open http://localhost:8000 in your browser.