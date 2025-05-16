import os
import sys

from jinja2 import Environment, FileSystemLoader

if len(sys.argv) != 3:
    print("Usage: python render_template.py <input_template_path> <output_file_path>")
    sys.exit(1)

template_file_path = sys.argv[1]
output_file_path = sys.argv[2]

template_dir = os.path.dirname(os.path.abspath(template_file_path))
template_filename = os.path.basename(template_file_path)

env = Environment(loader=FileSystemLoader(searchpath=template_dir))

template = env.get_template(template_filename)

rendered_content = template.render()
with open(output_file_path, "w") as f:
    f.write(rendered_content)

print(f"Template '{template_file_path}' rendered successfully to '{output_file_path}'")
