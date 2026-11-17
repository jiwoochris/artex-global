import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import { siGithub } from "simple-icons";
import { describe, expect, it } from "vitest";

import { SimpleIcon } from "./simple-icon";

describe("SimpleIcon", () => {
  it("renders an accessible svg from a simple-icons record", () => {
    render(<SimpleIcon icon={siGithub} />);
    const svg = screen.getByLabelText(siGithub.title);
    expect(svg).toBeInTheDocument();
    expect(svg).toHaveAttribute("viewBox", "0 0 24 24");
    expect(svg.querySelector("path")).toHaveAttribute("d", siGithub.path);
  });
});
